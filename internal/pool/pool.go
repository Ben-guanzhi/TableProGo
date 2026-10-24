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
	mu    sync.Mutex
	open  Opener
	max   int
	pools map[string]*sql.DB
	order []string // least-recently-used first
}

// New returns a cache that opens pools through open and keeps at most max of
// them (0 means DefaultMax).
func New(open Opener, max int) *Set {
	if max <= 0 {
		max = DefaultMax
	}
	return &Set{open: open, max: max, pools: make(map[string]*sql.DB)}
}

// Get returns the pool for database, opening it on first use. Errors from the
// opener are returned unchanged so the caller can add its own context.
func (s *Set) Get(database string) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if db, ok := s.pools[database]; ok {
		s.touchLocked(database)
		return db, nil
	}
	if s.open == nil {
		return nil, fmt.Errorf("pool: no opener configured")
	}
	db, err := s.open(database)
	if err != nil {
		return nil, err
	}
	// Browsing a server with hundreds of databases must not accumulate one live
	// pool per database; the oldest-unused pool is closed instead.
	for len(s.pools) >= s.max {
		s.evictOldestLocked()
	}
	s.pools[database] = db
	s.order = append(s.order, database)
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
