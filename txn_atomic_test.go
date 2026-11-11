package main

// The begin-tx route must answer with the transaction status AND the structured
// countdown detail in one round trip. The frontend used to issue Begin then a
// separate status read as two awaits in one try block: a Begin that succeeded
// followed by a failed second (the concurrency gate's 503, a hiccup) left the
// transaction open on the server with no badge in the UI — the 30s deadline
// calibrator only re-syncs tabs that already carry a badge, so it stayed
// invisible until the sweeper rolled it back and the user's edits "vanished".
// The atom is provable without a live database (SQLite holds transactions fine),
// so this is a plain unit test, not a TABULA_LIVE_PG one.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newSQLiteTxApp builds a minimal app over a real in-process SQLite file: the
// begin-tx atom is provable without a live PostgreSQL, and SQLite's own
// transactions make the round trips real rather than stubbed.
func newSQLiteTxApp(t *testing.T) (*App, *Connection) {
	t.Helper()
	cfg := ConnectionConfig{
		ID:       "tx-atomic",
		Name:     "tx-atomic",
		Type:     "sqlite",
		Database: filepath.Join(t.TempDir(), "tx.db"),
	}
	db, err := createConnection(cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		// The manager registry is process-wide; drop this connection's
		// managers before the pool goes away, exactly as Disconnect does.
		closeTxns(cfg.ID)
		db.Close()
	})
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tx_probe (id integer primary key)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	conn := &Connection{
		ID:       cfg.ID,
		Name:     cfg.Name,
		Type:     cfg.Type,
		Database: cfg.Database,
		Config:   cfg,
		DB:       db,
	}
	a := &App{
		connections: map[string]*Connection{conn.ID: conn},
		settings:    AppSettings{TxTimeoutMinutes: 10},
	}
	// sessionOf needs no seeding (it lazily creates), and the routes builder
	// wires the begin-tx endpoint this test exercises.
	return a, conn
}

var _ = sql.ErrNoRows // keep database/sql imported even if the helper stops using it

func TestBeginTxAnswersStatusAndDetailTogether(t *testing.T) {
	a, conn := newSQLiteTxApp(t)
	mux := a.routes()

	out := txPost(t, mux, "/api/begin-tx", map[string]string{
		"connId": conn.ID, "tabId": "atomic1",
	})

	status, _ := out["status"].(string)
	if status == "" {
		t.Fatalf("begin returned %v, want a status", out)
	}
	detail, ok := out["detail"].(map[string]any)
	if !ok {
		t.Fatalf("begin response missing the detail object: %v", out)
	}
	if got, _ := detail["status"].(string); got == "" {
		t.Fatalf("detail.status = %v, want non-empty", detail)
	}
	// The deadline was computed at Begin; the detail's remainingMs must carry
	// it (TxTimeoutMinutes>0 in the app settings), so the countdown starts
	// without a second call.
	ms, ok := detail["remainingMs"].(float64)
	if !ok || ms <= 0 {
		t.Fatalf("detail.remainingMs = %v, want a positive deadline", detail["remainingMs"])
	}

	// The detail read after Begin is consistent: same status the route answered.
	got := a.TransactionStatusDetail("atomic1")
	if got.Status != status {
		t.Fatalf("detail status %q != begin status %q", got.Status, status)
	}
	if got.Remaining <= 0 {
		t.Fatalf("detail remaining = %d, want positive", got.Remaining)
	}
}

func TestBeginTxTwiceIsRejectedButDetailStillRenders(t *testing.T) {
	a, conn := newSQLiteTxApp(t)
	mux := a.routes()

	first := txPost(t, mux, "/api/begin-tx", map[string]string{"connId": conn.ID, "tabId": "dup"})
	s1, _ := first["status"].(string)
	if s1 == "" {
		t.Fatalf("first begin returned %v, want an open status", first)
	}
	// The session's own ruling: a second Begin on the same tab is an error,
	// not a silent re-open (the user may believe they started a fresh one).
	if _, err := json.Marshal(map[string]string{"connId": conn.ID, "tabId": "dup"}); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/begin-tx", bytes.NewReader([]byte(`{"connId":"`+conn.ID+`","tabId":"dup"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatalf("double begin must be rejected, got 200: %s", rec.Body.String())
	}
	// The first response carried the detail; the countdown renders from it.
	if _, ok := first["detail"].(map[string]any); !ok {
		t.Fatalf("first begin missing detail: %v", first)
	}
}

// The failure path stays a failure: an unknown connection id returns the error
// status (HTTP 400) rather than a "success" with an empty detail.
func TestBeginTxUnknownConnStaysAnError(t *testing.T) {
	a, _ := newSQLiteTxApp(t)
	mux := a.routes()

	raw, err := json.Marshal(map[string]string{"connId": "nope", "tabId": "e1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/begin-tx", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatalf("unknown conn id must fail, got 200: %s", rec.Body.String())
	}
}
