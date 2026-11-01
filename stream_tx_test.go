package main

// A streamed statement must participate in the tab's transaction exactly like
// a buffered one: the stream reads through the transaction's own handle (so it
// sees the transaction's uncommitted writes), a failed stream marks the
// session aborted, and the buffered fallback runs as the tab's own statement.

import (
	"context"
	"testing"
)

// collectStreamTab is collectStream with the owning tab recorded, so the
// statement lands in that tab's transaction.
func collectStreamTab(t *testing.T, app *App, tabID, query string) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	err := app.StreamQuery(context.Background(), "c1", tabID, query, "", func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return events
}

// streamLastRows returns the rows of the last event that carried any (for a
// single-row count query, the only one).
func streamLastRows(events []StreamEvent) [][]interface{} {
	for i := len(events) - 1; i >= 0; i-- {
		if len(events[i].Rows) > 0 {
			return events[i].Rows
		}
	}
	return nil
}

func TestStreamReadsInsideOpenTransaction(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)

	if _, err := app.BeginTransaction("c1", "tabTx", ""); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if r := app.ExecuteQueryInTab("c1", "tabTx", "INSERT INTO t(id,name) VALUES (10,'tx')", ""); r.Error != "" {
		t.Fatalf("insert in tx: %s", r.Error)
	}

	// The streamed SELECT must see the transaction's uncommitted row: a stream
	// that read from the pool would see only the two seeded rows.
	events := collectStreamTab(t, app, "tabTx", "SELECT count(*) FROM t")
	done := events[len(events)-1]
	if done.Type != "done" || done.Error != "" {
		t.Fatalf("done = %+v", done)
	}
	rows := streamLastRows(events)
	if len(rows) != 1 || rows[0][0] != int64(3) {
		t.Fatalf("stream count inside tx = %v, want 3", rows)
	}

	// Rolling back takes the row away again, and the next stream (now in
	// autocommit) sees the committed state only.
	if _, err := app.RollbackTransaction("c1", "tabTx"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	events = collectStreamTab(t, app, "tabTx", "SELECT count(*) FROM t")
	rows = streamLastRows(events)
	if len(rows) != 1 || rows[0][0] != int64(2) {
		t.Fatalf("stream count after rollback = %v, want 2", rows)
	}
}

// A streamed write goes through the buffered fallback, which must run as the
// tab's own statement: inside the open transaction, with its failure marking
// the session.
func TestStreamFailureInsideTransactionMarksSession(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)

	if _, err := app.BeginTransaction("c1", "tabFail", ""); err != nil {
		t.Fatalf("begin: %v", err)
	}
	// id 1 is seeded: the duplicate key fails the statement inside the tx.
	events := collectStreamTab(t, app, "tabFail", "INSERT INTO t(id,name) VALUES (1,'dup')")
	done := events[len(events)-1]
	if done.Type != "done" || done.Error == "" {
		t.Fatalf("done = %+v, want the duplicate-key failure", done)
	}

	// The session must remember the failure: the next statement is refused
	// until the user rolls back, exactly as after a buffered failure.
	if r := app.ExecuteQueryInTab("c1", "tabFail", "INSERT INTO t(id,name) VALUES (5,'x')", ""); r.Error == "" {
		t.Fatal("a statement after the aborted stream must be refused")
	}
	if _, err := app.RollbackTransaction("c1", "tabFail"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if r := app.ExecuteQueryInTab("c1", "tabFail", "INSERT INTO t(id,name) VALUES (5,'x')", ""); r.Error != "" {
		t.Fatalf("autocommit after rollback: %s", r.Error)
	}
}

// A streamed read for a database other than the one the transaction is bound
// to is refused before anything runs, like the buffered path refuses it.
func TestStreamGuardRefusesOtherDatabase(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)

	if _, err := app.BeginTransaction("c1", "tabGuard", "other"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	var events []StreamEvent
	err := app.StreamQuery(context.Background(), "c1", "tabGuard", "SELECT 1", "elsewhere", func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(events) != 1 || events[0].Type != "error" || events[0].Error == "" {
		t.Fatalf("events = %+v, want a single refusal", events)
	}
}
