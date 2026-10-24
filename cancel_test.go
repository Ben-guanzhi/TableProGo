package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// beginRun must register the statement so CancelQuery can find and count it.
func TestCancelQueryReportsRunning(t *testing.T) {
	app := &App{}
	_, done := app.beginRun("c1")
	if got := app.CancelQuery("c1"); got != 1 {
		t.Fatalf("CancelQuery = %d, want 1", got)
	}
	done()
	if got := app.CancelQuery("c1"); got != 0 {
		t.Fatalf("after done, CancelQuery = %d, want 0", got)
	}
	// Cancelling a connection with nothing running is a no-op, not a panic.
	if got := app.CancelQuery("nope"); got != 0 {
		t.Fatalf("unknown connection = %d", got)
	}
}

// newLivePGApp connects one saved PostgreSQL connection, for tests that need a
// server-side statement long enough to cancel.
func newLivePGApp(t *testing.T) *App {
	t.Helper()
	raw := os.Getenv("TABULA_LIVE_PG")
	if raw == "" {
		t.Skip("TABULA_LIVE_PG not set")
	}
	cfg := connectionConfigFromURL(t, "postgresql", raw)
	cfg.ID = "c1"
	cfg.Name = "live"
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: []ConnectionConfig{cfg},
		settings:         AppSettings{ResultLimit: 1000},
	}
	if err := app.Connect("c1"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = app.Disconnect("c1") })
	return app
}

// CancelQuery must abort a statement that is actually running on the server:
// the context cancellation reaches pgx, which closes the connection and the
// query returns an error instead of the rows.
func TestLiveCancelStopsStatement(t *testing.T) {
	app := newLivePGApp(t)

	type outcome struct {
		result QueryResult
		took   time.Duration
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		result := app.ExecuteQuery("c1", "SELECT pg_sleep(30)", "")
		done <- outcome{result: result, took: time.Since(start)}
	}()

	// Wait until the statement is registered, then cancel it.
	deadline := time.Now().Add(10 * time.Second)
	cancelled := 0
	for time.Now().Before(deadline) {
		if cancelled = app.CancelQuery("c1"); cancelled > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cancelled == 0 {
		t.Fatal("the running statement was never registered for cancellation")
	}

	select {
	case got := <-done:
		if got.result.Error == "" {
			t.Fatalf("expected a cancellation error, got rows=%d", got.result.RowCount)
		}
		if !strings.Contains(strings.ToLower(got.result.Error), "cancel") &&
			!strings.Contains(strings.ToLower(got.result.Error), "context") {
			t.Fatalf("error does not look like a cancellation: %s", got.result.Error)
		}
		if got.took > 5*time.Second {
			t.Fatalf("cancellation took %v (statement ran to completion?)", got.took)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("statement did not return after CancelQuery")
	}

	// The connection stays usable afterwards: a fresh statement must work.
	if r := app.ExecuteQuery("c1", "SELECT 1", ""); r.Error != "" || r.RowCount != 1 {
		t.Fatalf("connection unusable after cancel: %+v", r)
	}
}
