package main

// HTTP-level tests for the transaction routes: a client must be able to begin,
// run statements in the tab, and commit or roll back, all over the JSON API the
// webview actually uses. The live database is WSL Docker PostgreSQL.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func txPost(t *testing.T, mux *http.ServeMux, path string, body any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: status %d, body %s", path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: decode %s: %v", path, rec.Body.String(), err)
	}
	return out
}

func TestTransactionRoutesOverHTTP(t *testing.T) {
	if os.Getenv("TABULA_LIVE_PG") == "" {
		t.Skip("set TABULA_LIVE_PG to run the transaction HTTP test")
	}
	a, conn := setupTxApp(t)
	mux := a.routes()

	// Begin
	out := txPost(t, mux, "/api/begin-tx", map[string]string{"connId": conn.ID, "tabId": "http1"})
	if status, _ := out["status"].(string); status == "" {
		t.Fatalf("begin returned %v, want an open transaction", out)
	}

	// A statement inside the transaction
	out = txPost(t, mux, "/api/execute-in-tab", map[string]string{
		"connId": conn.ID, "tabId": "http1", "query": "INSERT INTO tx_probe (id) VALUES (100)",
	})
	if errText, _ := out["Error"].(string); errText != "" {
		t.Fatalf("insert: %v", out)
	}
	// Not visible outside yet.
	if got := countProbe(t, a, conn); got != 0 {
		t.Fatalf("uncommitted row visible: %d", got)
	}

	// Commit, then it is.
	txPost(t, mux, "/api/commit-tx", map[string]string{"connId": conn.ID, "tabId": "http1"})
	if got := countProbe(t, a, conn); got != 1 {
		t.Fatalf("after commit count = %d", got)
	}

	// A second transaction, rolled back.
	txPost(t, mux, "/api/begin-tx", map[string]string{"connId": conn.ID, "tabId": "http2"})
	txPost(t, mux, "/api/execute-in-tab", map[string]string{
		"connId": conn.ID, "tabId": "http2", "query": "INSERT INTO tx_probe (id) VALUES (200)",
	})
	txPost(t, mux, "/api/rollback-tx", map[string]string{"connId": conn.ID, "tabId": "http2"})
	if got := countProbe(t, a, conn); got != 1 {
		t.Fatalf("rollback changed the count: %d", got)
	}
}

// A tab with no transaction still runs through the tab route (autocommit), which is
// what every ordinary editor tab does.
func TestTabRouteWithoutTransactionIsAutocommit(t *testing.T) {
	if os.Getenv("TABULA_LIVE_PG") == "" {
		t.Skip("set TABULA_LIVE_PG to run the transaction HTTP test")
	}
	a, conn := setupTxApp(t)
	mux := a.routes()

	out := txPost(t, mux, "/api/execute-in-tab", map[string]string{
		"connId": conn.ID, "tabId": "plain", "query": "INSERT INTO tx_probe (id) VALUES (5)",
	})
	if errText, _ := out["Error"].(string); errText != "" {
		t.Fatalf("insert: %v", out)
	}
	if got := countProbe(t, a, conn); got != 1 {
		t.Fatalf("autocommit insert not visible: %d", got)
	}
}
