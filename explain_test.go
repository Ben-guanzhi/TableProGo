package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// explainFixture is the minimal App wiring an explain needs: one open SQLite
// connection holding two rows that a plan must not touch.
func explainFixture(t *testing.T) *App {
	t.Helper()
	path := filepath.Join(t.TempDir(), "explain.db")
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: []ConnectionConfig{{ID: "c1", Name: "explain", Type: "sqlite", Database: path}},
		settings:         AppSettings{SafeMode: safeModeOff, ResultLimit: 1000},
	}
	if err := app.Connect("c1"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = app.Disconnect("c1") })
	for _, q := range []string{
		"CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)",
		"INSERT INTO t(id,name) VALUES (1,'a'),(2,'b')",
	} {
		if r := app.ExecuteQuery("c1", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}
	return app
}

// A plan for a SELECT comes back as a grid (SQLite's EXPLAIN QUERY PLAN calls
// its column "detail") and leaves the rows it "would have read" alone.
func TestExplainSQLiteReturnsPlanWithoutExecuting(t *testing.T) {
	app := explainFixture(t)

	// SQLite's EXPLAIN QUERY PLAN answers with the columns id/parent/notused
	// and the human-readable plan in "detail".
	r := app.ExplainQuery("c1", "SELECT * FROM t WHERE id = 1", "")
	if r.Error != "" {
		t.Fatalf("explain: %s", r.Error)
	}
	hasDetail := false
	for _, c := range r.Columns {
		hasDetail = hasDetail || c.Name == "detail"
	}
	if !hasDetail {
		t.Fatalf("columns = %+v, want the plan's detail column", r.Columns)
	}
	if r.RowCount == 0 {
		t.Fatal("plan grid is empty")
	}
	if r.AffectedRows != 0 {
		t.Fatalf("affected rows = %d, want 0", r.AffectedRows)
	}
	if got := rowCount(t, app); got != 2 {
		t.Fatalf("rows after explain = %d, want the untouched 2", got)
	}
}

// A plan for a write statement must neither run the write nor trip Safe Mode:
// nothing executes, so even blockNoWhere lets a WHERE-less DELETE be planned.
func TestExplainSQLitePlansWriteWithoutRunningIt(t *testing.T) {
	app := explainFixture(t)

	r := app.ExplainQuery("c1", "DELETE FROM t WHERE id = 1", "")
	if r.Error != "" {
		t.Fatalf("explain: %s", r.Error)
	}
	if r.RowCount == 0 {
		t.Fatal("plan grid is empty")
	}
	if r.AffectedRows != 0 {
		t.Fatalf("affected rows = %d, want 0", r.AffectedRows)
	}
	if got := rowCount(t, app); got != 2 {
		t.Fatalf("rows after explaining a DELETE = %d, want the untouched 2", got)
	}
}

// An empty statement is a caller mistake, not something to send to the engine.
func TestExplainSQLiteRejectsEmptyStatement(t *testing.T) {
	app := explainFixture(t)

	r := app.ExplainQuery("c1", "   ", "")
	if r.Error == "" || !strings.Contains(r.Error, "statement") {
		t.Fatalf("expected an empty-statement error, got %+v", r)
	}
}
