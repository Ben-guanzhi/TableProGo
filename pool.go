package main

// Cross-database pools live in internal/pool; this file keeps the engine rules
// (which databases need a session of their own) and the connection wiring.

import (
	"database/sql"
	"fmt"

	"tablepro-go/internal/pool"
)

// poolFor returns the *sql.DB to introspect the given database with. PostgreSQL
// cannot see other databases over an existing connection, so those get a lazily
// opened, cached pool of their own; the other engines answer metadata queries
// through qualified names on the main pool, which keeps browsing the tree from
// opening one connection per database.
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

// perDatabasePool is the connection's pool cache, wired to this app's opener.
// The opener copies the saved config, swaps the database and dials through the
// same SSH tunnel the main session uses.
func (a *App) perDatabasePool(conn *Connection, database string) (*sql.DB, error) {
	conn.ensurePools()
	db, err := conn.pools.Get(database)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", database, err)
	}
	return db, nil
}

// ensurePools lazily builds the cache. The opener closes over conn so it always
// reads the current tunnel endpoint and saved config.
func (c *Connection) ensurePools() {
	c.poolsOnce.Do(func() {
		c.pools = pool.New(func(database string) (*sql.DB, error) {
			config := c.Config
			config.Database = database
			host, port := c.dialTarget()
			db, err := createConnectionAt(config, host, port)
			if err != nil {
				return nil, err
			}
			if err := db.Ping(); err != nil {
				_ = db.Close()
				return nil, err
			}
			return db, nil
		}, maxPoolsPerConnection)
	})
}

func (c *Connection) closePools() {
	if c.pools != nil {
		c.pools.Close()
	}
}
