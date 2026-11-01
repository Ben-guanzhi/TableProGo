package main

// Live proof of the zero-row UPDATE verdict on the engine where it matters.
//
// MySQL counts only changed rows: an UPDATE that matched its key but wrote
// identical values reports zero affected rows, exactly like an UPDATE whose row
// vanished. The apply path probes the key to tell the two apart, and only a real
// server shows that the probe reads the right state inside the transaction.
//
// Skipped unless TABULA_LIVE_MYSQL is set.

import (
	"os"
	"strings"
	"testing"
)

func TestLiveApplyNoOpUpdateOnMySQL(t *testing.T) {
	raw := os.Getenv("TABULA_LIVE_MYSQL")
	if raw == "" {
		t.Skip("set TABULA_LIVE_MYSQL to run the live apply test")
	}
	cfg := connectionConfigFromURL(t, "mysql", raw)
	db, err := createConnection(cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		closeTxns("mysql-apply-live")
		db.Close()
	})
	conn := &Connection{
		ID:       "mysql-apply-live",
		Name:     "mysql-apply-live",
		Type:     "mysql",
		Database: cfg.Database,
		Config:   cfg,
		DB:       db,
	}
	if _, err := conn.DB.Exec(`DROP TABLE IF EXISTS apply_probe`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := conn.DB.Exec(`CREATE TABLE apply_probe (id integer primary key, name varchar(50), qty integer)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO apply_probe (id, name, qty) VALUES (1,'a',10)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { conn.DB.Exec(`DROP TABLE IF EXISTS apply_probe`) })

	a := searchApp(conn)

	// qty already holds 10: the UPDATE matches its row and changes nothing.
	// MySQL reports zero affected rows for it; the key probe must let the
	// batch through instead of refusing it as "matched no row".
	if _, err := a.ApplyChanges(ChangeSet{
		ConnID:   conn.ID,
		Database: conn.Database,
		Table:    "apply_probe",
		Columns:  []string{"id", "name", "qty"},
		Changes: []RowChange{
			{Original: []interface{}{1.0, "a", 10.0}, Edits: []CellEdit{{Column: "qty", Value: "10"}}},
		},
	}); err != nil {
		t.Fatalf("a no-op update must not abort the batch on MySQL: %v", err)
	}

	// The inverse must still refuse: an UPDATE of a row another statement in
	// the same batch deleted is a vanished row, not a no-op.
	_, err = a.ApplyChanges(ChangeSet{
		ConnID:   conn.ID,
		Database: conn.Database,
		Table:    "apply_probe",
		Columns:  []string{"id", "name", "qty"},
		Changes: []RowChange{
			{Kind: rowDelete, Original: []interface{}{1.0, "a", 10.0}},
			{Original: []interface{}{1.0, "a", 10.0}, Edits: []CellEdit{{Column: "qty", Value: "99"}}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "matched no row") {
		t.Fatalf("want a refused update of a vanished row, got %v", err)
	}
}
