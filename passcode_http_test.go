package main

// Route-level tests for the local-passcode surface (P2-9). The mux is used
// without the auth wrapper (the pattern txn_api_test.go follows): these tests
// pin the JSON contract the frontend codes against.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func passcodeApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	app.passcodePath = filepath.Join(t.TempDir(), "passcode.json")
	return app
}

func postJSON(t *testing.T, mux http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPasscodeRoutes(t *testing.T) {
	app := passcodeApp(t)
	mux := app.routes()

	rec := postJSON(t, mux, "/api/passcode/has", `{}`)
	var has struct {
		Has bool `json:"has"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &has); err != nil {
		t.Fatalf("has: %v (%q)", err, rec.Body.String())
	}
	if has.Has {
		t.Fatal("has=true on a fresh app")
	}

	rec = postJSON(t, mux, "/api/passcode/set", `{"passcode":"12"}`)
	if rec.Code == http.StatusOK {
		t.Fatal("a 2-char passcode was accepted")
	}
	rec = postJSON(t, mux, "/api/passcode/set", `{"passcode":"correct-horse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set: %d %q", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, mux, "/api/passcode/verify", `{"passcode":"wrong-one"}`)
	var ok struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok.OK {
		t.Fatal("verify returned ok for the wrong passcode")
	}
	rec = postJSON(t, mux, "/api/passcode/verify", `{"passcode":"correct-horse"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil || !ok.OK {
		t.Fatalf("verify right passcode: ok=%+v body=%q", ok, rec.Body.String())
	}

	rec = postJSON(t, mux, "/api/passcode/clear", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %q", rec.Code, rec.Body.String())
	}
	if app.HasPasscode() {
		t.Fatal("the passcode survived the clear route")
	}
}

// The idle probe answers even where the OS reports nothing (-1 = "fall back
// to page activity"), so the frontend watcher never has to special-case a
// platform.
func TestSystemIdleRoute(t *testing.T) {
	app := passcodeApp(t)
	mux := app.routes()
	rec := postJSON(t, mux, "/api/system-idle", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("idle: %d %q", rec.Code, rec.Body.String())
	}
	var out struct {
		Seconds int `json:"seconds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("idle json: %v (%q)", err, rec.Body.String())
	}
	if out.Seconds < -1 {
		t.Fatalf("seconds = %d, want >= -1", out.Seconds)
	}
}

// DisconnectAll closes every connection; the lock calls it so nothing stays
// reachable behind the lock screen. An app with no connections answers ok all
// the same (locking a fresh window must not error).
func TestDisconnectAllRoute(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if _, err := app.connections["c1"].DB.Exec(`SELECT 1`); err != nil {
		t.Fatalf("connection not live: %v", err)
	}
	mux := app.routes()
	rec := postJSON(t, mux, "/api/disconnect-all", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disconnect-all: %d %q", rec.Code, rec.Body.String())
	}
	// Every pooled connection is closed: Disconnect drops the pool handle, so
	// the entry survives but holds no live DB.
	if app.connections["c1"].DB != nil {
		t.Fatal("the connection still holds a live DB after DisconnectAll")
	}
	// A second call is harmless, not an error.
	rec = postJSON(t, mux, "/api/disconnect-all", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("second disconnect-all: %d", rec.Code)
	}
}

// Disconnect must publish a fresh handle rather than nilling DB/KV on the
// shared *Connection: readers that already fetched the object keep reading
// fields nobody writes after teardown, which is the connection lock
// discipline's core requirement. This test pins both halves - the entry
// survives with a nil DB, and the old object is not the one in the map.
func TestDisconnectSwapsHandleWithoutMutatingInPlace(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	old := app.connections["c1"]
	if _, err := old.DB.Exec(`SELECT 1`); err != nil {
		t.Fatalf("connection not live: %v", err)
	}
	if err := app.Disconnect("c1"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if got := app.connections["c1"]; got == old {
		t.Fatal("Disconnect mutated the shared *Connection in place; it must publish a fresh handle")
	}
	if got := app.connections["c1"]; got.DB != nil || got.KV != nil {
		t.Fatal("the surviving handle still holds a live driver")
	}
	// The old object is immutable now: its DB points at a closed pool, so an
	// in-flight reader gets an error instead of racing the teardown.
	if _, err := old.DB.Exec(`SELECT 1`); err == nil {
		t.Fatal("the old handle still answered a statement after disconnect")
	}
	// Identity survives the swap so the sidebar still renders the connection.
	if got := app.connections["c1"]; got.Name != "safe" || got.Type != "sqlite" {
		t.Fatalf("handle identity lost: name=%q type=%q", got.Name, got.Type)
	}
}
