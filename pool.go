package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"
)

const (
	// poolCloseGrace bounds how long an evicted pool waits for its in-flight
	// statements to finish before it is closed anyway.
	poolCloseGrace = 30 * time.Second
	poolCloseTick  = 200 * time.Millisecond
)

// poolFor returns the *sql.DB to introspect the given database with. PostgreSQL
// cannot see other databases over an existing connection, so those get a
// lazily opened, cached pool of their own; the other engines answer metadata
// queries through qualified names on the main pool, which keeps browsing the
// tree from opening one connection per database.
func (a *App) poolFor(conn *Connection, database string) (*sql.DB, error) {
	if conn.DB == nil {
		return nil, fmt.Errorf("not connected")
	}
	if conn.Type == "postgresql" && database != "" && database != conn.Database {
		return a.perDatabasePool(conn, database)
	}
	return conn.DB, nil
}

// queryPool returns the pool a statement runs on. Unlike metadata lookups a
// statement resolves unqualified table names against the session's own
// database, so a tab that targets another one needs that database opened as a
// session of its own.
func (a *App) queryPool(conn *Connection, database string) (*sql.DB, error) {
	if database != "" && database != conn.Database {
		switch conn.Type {
		case "mysql", "sqlserver":
			return a.perDatabasePool(conn, database)
		}
	}
	return a.poolFor(conn, database)
}

func (a *App) perDatabasePool(conn *Connection, database string) (*sql.DB, error) {
	conn.poolsMu.Lock()
	defer conn.poolsMu.Unlock()
	if db, ok := conn.pools[database]; ok {
		conn.touchPool(database)
		return db, nil
	}

	config := conn.Config
	config.Database = database
	db, err := createConnection(config)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %q: %w", database, err)
	}
	if conn.pools == nil {
		conn.pools = make(map[string]*sql.DB)
	}
	// Browsing a server with hundreds of databases must not accumulate one
	// live pool per database; the oldest-unused pool is closed instead.
	for len(conn.pools) >= maxPoolsPerConnection {
		conn.evictOldestPool()
	}
	conn.pools[database] = db
	conn.poolOrder = append(conn.poolOrder, database)
	return db, nil
}

// touchPool moves database to the most-recently-used end of the LRU order.
// Callers hold poolsMu.
func (c *Connection) touchPool(database string) {
	for i, name := range c.poolOrder {
		if name == database {
			c.poolOrder = append(c.poolOrder[:i], c.poolOrder[i+1:]...)
			break
		}
	}
	c.poolOrder = append(c.poolOrder, database)
}

// evictOldestPool fences off the least-recently-used cached pool and closes it
// once no statement is running on it. Callers hold poolsMu.
//
// poolFor hands the *sql.DB to callers with no reference count, so an immediate
// Close could land inside a statement still reading from the pool. Fencing
// stops the pool growing in the meantime: no new idle connections are kept and
// no connection survives a single use.
func (c *Connection) evictOldestPool() {
	if len(c.poolOrder) == 0 {
		return
	}
	oldest := c.poolOrder[0]
	c.poolOrder = c.poolOrder[1:]
	db, ok := c.pools[oldest]
	if !ok {
		return
	}
	delete(c.pools, oldest)
	db.SetConnMaxLifetime(time.Nanosecond)
	db.SetMaxIdleConns(0)
	go closePoolWhenIdle(oldest, db)
}

// closePoolWhenIdle waits for the pool to drain and then closes it. Giving up
// after poolCloseGrace bounds the memory a permanently stalled statement can
// hold; past that the pool is closed anyway, which fails the stalled statement
// rather than leaking the pool forever.
func closePoolWhenIdle(name string, db *sql.DB) {
	deadline := time.Now().Add(poolCloseGrace)
	for time.Now().Before(deadline) {
		if db.Stats().InUse == 0 {
			if err := db.Close(); err != nil {
				log.Printf("close evicted pool %q: %v", name, err)
			}
			return
		}
		time.Sleep(poolCloseTick)
	}
	if err := db.Close(); err != nil {
		log.Printf("close evicted pool %q after %v: %v", name, poolCloseGrace, err)
	}
}

func (c *Connection) closePools() {
	c.poolsMu.Lock()
	defer c.poolsMu.Unlock()
	for name, db := range c.pools {
		db.Close()
		delete(c.pools, name)
	}
	c.poolOrder = nil
}
