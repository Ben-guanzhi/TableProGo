package main

import (
	"context"
	"testing"
)

// SQLite `:memory:` (and an empty File, which the kernel DSN renders as
// ":memory:") is per-connection. A pool of >1 would let concurrent statements
// land on distinct physical handles — each seeing its own empty database, so
// a table created moments earlier on another handle would be invisible. Pin the
// pool to one connection, matching polydb's dbsqlite driver.
func TestCreateConnectionAtMemoryPoolIsPinned(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		cfg  ConnectionConfig
	}{
		{"memory explicit", ConnectionConfig{Type: "sqlite", Database: ":memory:"}},
		{"memory empty database", ConnectionConfig{Type: "sqlite"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, err := createConnectionAt(c.cfg, c.cfg.Host, c.cfg.Port)
			if err != nil {
				t.Fatalf("createConnectionAt: %v", err)
			}
			defer db.Close()
			stats := db.Stats()
			if stats.MaxOpenConnections != 1 {
				t.Errorf("MaxOpenConnections = %d, want 1 (per-connection :memory: must not fork)",
					stats.MaxOpenConnections)
			}
			// The pool must be functional: a CREATE followed by a SELECT on the
			// same pinned handle sees the table.
			if _, err := db.ExecContext(ctx, "CREATE TABLE mem_probe(v INTEGER)"); err != nil {
				t.Fatalf("create table on pinned pool: %v", err)
			}
			var n int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mem_probe").Scan(&n); err != nil {
				t.Fatalf("query on pinned pool: %v", err)
			}
		})
	}
}

// A file-backed SQLite database may keep the normal pool size — only the
// per-connection in-memory form forks.
func TestCreateConnectionAtFilePoolIsNotPinned(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/pool.db"
	db, err := createConnectionAt(ConnectionConfig{Type: "sqlite", Database: path}, "", 0)
	if err != nil {
		t.Fatalf("createConnectionAt: %v", err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got != dbMaxOpenConns {
		t.Errorf("file DB MaxOpenConnections = %d, want %d (only :memory: is pinned)",
			got, dbMaxOpenConns)
	}
}
