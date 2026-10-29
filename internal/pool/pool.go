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
// The dial runs OUTSIDE the lock: an opener dials and pings (up to the
// connection timeout), and holding the cache lock through it queued every
// other database's Get on this connection behind one slow server — an ER
// diagram walk touching dozens of databases froze on the first unreachable
// one. Same-database callers join the in-flight dial instead of racing their
// own.
func (s *Set) Get(database string) (*sql.DB, error) {
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
	call := &dialCall{done: make(chan struct{})}
	s.dialing[database] = call
	opener := s.open
	s.mu.Unlock()

	db, err := opener(database)

	s.mu.Lock()
	delete(s.dialing, database)
	if err == nil {
		// Browsing a server with hundreds of databases must not accumulate one
		// live pool per database; the oldest-unused pool is closed instead.
		for len(s.pools) >= s.max {
			s.evictOldestLocked()
		}
		s.pools[database] = db
		s.order = append(s.order, database)
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

// Close closes every cached pool.
func (s *Set) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, db := range s.pools {
		_ = db.Close()
		delete(s.pools, name)
	}
	s.order = nil
	// An in-flight dial that lands after Close just repopulates the map —
	// Close is not a tombstone (a fresh Get after it reopens), so the dial
	// result stays usable.
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
			return
		}
		time.Sleep(closeTick)
	}
	if err := db.Close(); err != nil {
		log.Printf("close evicted pool %q after %v: %v", name, CloseGrace, err)
	}
}
