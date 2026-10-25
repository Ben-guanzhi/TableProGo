package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Reconnecting an already-open connection closes the old pool. A transaction
// manager left in the registry would wrap that dead pool and its next commit
// would fail with "database is closed"; the reconnect must drop it so the tab
// sees the honest "transaction not found" instead.
func TestReconnectDropsStaleTxnManagers(t *testing.T) {
	cfg := ConnectionConfig{
		ID: "reconnect-txn-1", Name: "rc", Type: "sqlite",
		Database: filepath.Join(t.TempDir(), "reconnect.db"),
	}
	a := &App{
		connections:      map[string]*Connection{},
		savedConnections: []ConnectionConfig{cfg},
	}
	t.Cleanup(func() {
		// Release the SQLite file before TempDir removal (Windows keeps the
		// handle open until the pool is closed).
		_ = a.Disconnect(cfg.ID)
		closeTxns(cfg.ID)
	})

	if err := a.Connect(cfg.ID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := a.BeginTransaction(cfg.ID, "tab1", ""); err != nil {
		t.Fatalf("begin: %v", err)
	}

	if err := a.Connect(cfg.ID); err != nil {
		t.Fatalf("reconnect: %v", err)
	}

	_, err := a.CommitTransaction(cfg.ID, "tab1")
	if err == nil {
		t.Fatal("commit after reconnect should fail: the transaction was dropped with the old pool")
	}
	if strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("stale manager survived reconnect: %v", err)
	}
}
