// Package pool keeps one database/sql pool per database.
//
// PostgreSQL cannot see other databases over an existing connection, and MySQL
// and SQL Server resolve unqualified names against the session's own database, so
// a statement aimed at another database needs a session of its own. Opening one
// per request would pay a handshake each time, so each database gets a cached
// pool; browsing a server with hundreds of databases must not accumulate them, so
// the least-recently-used one is fenced off and closed once it drains.
//
// The package owns the cache policy only: how a pool is actually opened stays
// with the caller, which knows the engine, the DSN and whether a tunnel is in
// effect.
package pool

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	// DefaultMax is how many per-database pools one connection keeps live.
	DefaultMax = 32
	// CloseGrace bounds how long an evicted pool waits for its in-flight
	// statements before it is closed anyway.
	CloseGrace = 30 * time.Second
	closeTick  = 200 * time.Millisecond
)

var (
	// GlobalMaxPools bounds the total number of open pools across all
	// connections. Without it, N saved connections × 32 pools each × 10
	// connections per pool = up to 320×N server connections, which can
	// exhaust the server's connection limit. Each pool.Set checks this
	// global counter before opening a new pool. It is a var so tests can
	// lower the limit to exercise the admission gate.
	GlobalMaxPools = 128

	// GlobalGateTimeout bounds how long a Set waits at the global admission
	// gate after evicting its oldest pool. The eviction closes asynchronously
	// (closeWhenIdle drains up to CloseGrace), so a Set with no pools to
	// evict cannot make room and must fail rather than spin forever.
	GlobalGateTimeout = CloseGrace + 5*time.Second
)

// globalPools tracks the total number of open pools across all Set instances.
// It is the admission-control gate: a Set cannot open a new pool when the
// global count is at the limit, so the total server-connection footprint is
// bounded even when many connections are open.
var globalPools struct {
	mu    sync.Mutex
	count int
}

// globalDialGate bounds the number of concurrent dial+ping operations across
// all Set instances. Without it, an ER diagram walk touching hundreds of
// databases issues hundreds of parallel TCP handshakes + PINGs, spiking
// server load and possibly tripping rate limits / connection churn.
var globalDialGate = make(chan struct{}, 8)

// globalPoolCount returns the current total number of open pools.
func globalPoolCount() int {
	globalPools.mu.Lock()
	defer globalPools.mu.Unlock()
	return globalPools.count
}

// Opener opens a pool for one database. It is called with the database name the
// caller asked for and must return a connected pool.
type Opener func(database string) (*sql.DB, error)

// Set is the cache for one connection. The zero value is not usable; call New.
type Set struct {
	mu      sync.Mutex
	open    Opener
	max     int
	pools   map[string]*sql.DB
	order   []string // least-recently-used first
	dialing map[string]*dialCall
	// closeSeq bumps on every Close. A dial records the seq it started under;
	// when it lands under a different seq, the pools it saw were torn down and
	// its result is dropped instead of repopulating the map. A fresh Get after
	// Close picks up the new seq and works (Close only invalidates the dials
	// that overlapped it, not the cache itself).
	closeSeq int
}

// dialCall is one in-flight opener: same-database callers share the dial
// instead of each paying their own handshake.
type dialCall struct {
	done chan struct{}
	db   *sql.DB
	err  error
}

// New returns a cache that opens pools through open and keeps at most max of
// them (0 means DefaultMax).
func New(open Opener, max int) *Set {
	if max <= 0 {
		max = DefaultMax
	}
	return &Set{open: open, max: max, pools: make(map[string]*sql.DB), dialing: make(map[string]*dialCall)}
}

// Get returns the pool for database, opening it on first use. Errors from the
// opener are returned unchanged so the caller can add its own context.
//
// GetContext is the cancellation-aware form: the global admission gate and the
// bounded dial both wait on ctx instead of sleeping, so a caller cancellation
// (or a Close that lands during the wait) releases a waiter rather than leaving
// it to poll until GlobalGateTimeout (TP-11).
//
// The dial runs OUTSIDE the lock: an opener dials and pings (up to the
// connection timeout), and holding the cache lock through it queued every
// other database's Get on this connection behind one slow server — an ER
// diagram walk touching dozens of databases froze on the first unreachable
// one. Same-database callers join the in-flight dial instead of racing their
// own.
func (s *Set) Get(database string) (*sql.DB, error) {
	return s.GetContext(context.Background(), database)
}

func (s *Set) GetContext(ctx context.Context, database string) (*sql.DB, error) {
	s.mu.Lock()
	if db, ok := s.pools[database]; ok {
		s.touchLocked(database)
		s.mu.Unlock()
		return db, nil
	}
	if s.open == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("pool: no opener configured")
	}
	if call, ok := s.dialing[database]; ok {
		s.mu.Unlock()
		<-call.done
		// The dial's winner inserted the pool under the lock; handing the
		// call's own result covers the rare case where it was evicted in the
		// meantime (fencing keeps an evicted pool usable while it drains).
		return call.db, call.err
	}
	// A global admission gate: without it, N connections × 32 pools × 10
	// connections = up to 320×N server connections. When the global count is
	// at the limit, this Set evicts its oldest pool to make room.
	//
	// Evict at most one pool per Get: the eviction's close is asynchronous
	// (closeWhenIdle drains in the background), so the global count drops only
	// once the pool actually closes. Evicting once and waiting lets the count
	// fall instead of racing to evict every pool while they drain.
	//
	// The wait is cancellable (select on ctx and on time), not a bare sleep:
	// it still polls, but a caller cancellation or a Close that lands during
	// the wait returns immediately instead of burning the whole window.
	evicted := false
	gateDeadline := time.Now().Add(GlobalGateTimeout)
	waitSeq := s.closeSeq
	for globalPoolCount() >= GlobalMaxPools {
		if !evicted {
			s.evictOldestLocked()
			evicted = true
		}
		// Eviction fences the oldest pool (stops new use) but its close waits
		// for in-flight statements to drain, which can take up to CloseGrace.
		// The wait must not hold the cache lock: holding it through a 30s
		// drain queued every other database's Get — the same freeze the dial
		// outside the lock below already fixed. The lock is dropped for the
		// re-check; a concurrent evict only moves the count the same way.
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			return nil, fmt.Errorf("pool: %w", ctx.Err())
		case <-time.After(closeTick):
		}
		s.mu.Lock()
		if globalPoolCount() < GlobalMaxPools {
			break
		}
		if s.closeSeq != waitSeq {
			// A Close landed while we waited: the pools this gate was waiting
			// for are gone, so stop waiting rather than poll to the deadline.
			s.mu.Unlock()
			return nil, fmt.Errorf("pool: closed while waiting for the global admission gate")
		}
		if time.Now().After(gateDeadline) {
			s.mu.Unlock()
			return nil, fmt.Errorf("pool: global pool limit %d reached; evicted pools are still draining", GlobalMaxPools)
		}
	}

	call := &dialCall{done: make(chan struct{})}
	s.dialing[database] = call
	opener := s.open
	// Record the closeSeq this dial starts under, so a Close that lands while
	// the dial runs can drop the result instead of repopulating the map.
	dialSeq := s.closeSeq
	s.mu.Unlock()

	// The dial gate bounds concurrent dials across all connections: an ER
	// diagram walk touching hundreds of databases used to issue hundreds of
	// parallel TCP handshakes + PINGs. 8 at a time keeps the server load sane.
	// The wait is cancellable: a caller cancellation ends it without waiting
	// for a slot (TP-11).
	var db *sql.DB
	var err error
	select {
	case globalDialGate <- struct{}{}:
		db, err = opener(database)
		<-globalDialGate
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.dialing, database)
		call.db, call.err = nil, fmt.Errorf("pool: %w", ctx.Err())
		close(call.done)
		s.mu.Unlock()
		return nil, fmt.Errorf("pool: %w", ctx.Err())
	}

	s.mu.Lock()
	delete(s.dialing, database)
	if s.closeSeq != dialSeq {
		// Close happened while the dial ran: drop the result instead of
		// repopulating the map. The pools the dial saw were torn down.
		call.db, call.err = nil, fmt.Errorf("pool: closed during dial")
		close(call.done)
		s.mu.Unlock()
		if err == nil {
			_ = db.Close()
		}
		return nil, fmt.Errorf("pool: closed during dial")
	}
	if err == nil {
		// Browsing a server with hundreds of databases must not accumulate one
		// live pool per database; the oldest-unused pool is closed instead.
		for len(s.pools) >= s.max {
			s.evictOldestLocked()
		}
		s.pools[database] = db
		s.order = append(s.order, database)
		globalPools.mu.Lock()
		globalPools.count++
		globalPools.mu.Unlock()
	}
	call.db, call.err = db, err
	close(call.done)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return db, nil
}

// Len reports how many pools are cached (tests and diagnostics).
func (s *Set) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pools)
}

// Close closes every cached pool and bumps the closeSeq, which invalidates
// any dial in flight. A fresh Get after Close works (it picks up the new seq).
func (s *Set) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeSeq++
	for name, db := range s.pools {
		_ = db.Close()
		delete(s.pools, name)
		globalPools.mu.Lock()
		globalPools.count--
		globalPools.mu.Unlock()
	}
	s.order = nil
}

// touchLocked moves database to the most-recently-used end of the order.
func (s *Set) touchLocked(database string) {
	for i, name := range s.order {
		if name == database {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.order = append(s.order, database)
}

// evictOldestLocked fences off the least-recently-used pool and closes it once
// no statement is running on it. Callers hold mu.
//
// Get hands the *sql.DB to callers with no reference count, so an immediate
// Close could land inside a statement still reading from the pool. Fencing stops
// the pool growing in the meantime: no new idle connections are kept and no
// connection survives a single use.
func (s *Set) evictOldestLocked() {
	if len(s.order) == 0 {
		return
	}
	oldest := s.order[0]
	s.order = s.order[1:]
	db, ok := s.pools[oldest]
	if !ok {
		return
	}
	delete(s.pools, oldest)
	db.SetConnMaxLifetime(time.Nanosecond)
	db.SetMaxIdleConns(0)
	go closeWhenIdle(oldest, db)
}

// closeWhenIdle waits for the pool to drain and then closes it. Giving up after
// CloseGrace bounds the memory a permanently stalled statement can hold; past
// that the pool is closed anyway, which fails the stalled statement rather than
// leaking the pool forever.
func closeWhenIdle(name string, db *sql.DB) {
	deadline := time.Now().Add(CloseGrace)
	for time.Now().Before(deadline) {
		if db.Stats().InUse == 0 {
			if err := db.Close(); err != nil {
				log.Printf("close evicted pool %q: %v", name, err)
			}
			globalPools.mu.Lock()
			globalPools.count--
			globalPools.mu.Unlock()
			return
		}
		time.Sleep(closeTick)
	}
	if err := db.Close(); err != nil {
		log.Printf("close evicted pool %q after %v: %v", name, CloseGrace, err)
	}
	globalPools.mu.Lock()
	globalPools.count--
	globalPools.mu.Unlock()
}
