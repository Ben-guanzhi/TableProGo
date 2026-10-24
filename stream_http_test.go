package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The HTTP endpoint must answer NDJSON with one JSON object per line, in order:
// columns, rows, done. (The browser parses exactly this.)
func TestServeExecuteStreamWritesNDJSON(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)

	body := `{"connId":"c1","query":"SELECT id, name FROM t ORDER BY id","database":""}`
	req := httptest.NewRequest("POST", "/api/execute-stream", strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.serveExecuteStream(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content type = %q", ct)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d: %q", len(lines), rec.Body.String())
	}
	var first, second, third StreamEvent
	for i, line := range lines {
		var ev StreamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d is not JSON: %v (%q)", i, err, line)
		}
		switch i {
		case 0:
			first = ev
		case 1:
			second = ev
		case 2:
			third = ev
		}
	}
	if first.Type != "columns" || len(first.Columns) != 2 {
		t.Fatalf("first = %+v", first)
	}
	if second.Type != "rows" || len(second.Rows) != 2 {
		t.Fatalf("second = %+v", second)
	}
	if third.Type != "done" || third.RowCount != 2 || third.Error != "" {
		t.Fatalf("third = %+v", third)
	}
}

func TestServeExecuteStreamRejectsGet(t *testing.T) {
	app := &App{connections: map[string]*Connection{}}
	req := httptest.NewRequest("GET", "/api/execute-stream", nil)
	rec := httptest.NewRecorder()
	app.serveExecuteStream(rec, req)
	if rec.Code != 405 {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// A Safe Mode pause must arrive as an error event that says confirmation is
// possible, so the UI can offer the prompt instead of showing a dead end.
func TestServeExecuteStreamSurfacesSafeMode(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeConfirmNoWhere)
	body := `{"connId":"c1","query":"DELETE FROM t","database":""}`
	req := httptest.NewRequest("POST", "/api/execute-stream", strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.serveExecuteStream(rec, req)

	var ev StreamEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(rec.Body.String())), &ev); err != nil {
		t.Fatalf("decode: %v (%q)", err, rec.Body.String())
	}
	if ev.Type != "error" || !ev.NeedsConfirmation || ev.Error == "" {
		t.Fatalf("event = %+v", ev)
	}
	if r := app.ExecuteQuery("c1", "SELECT id FROM t", ""); r.RowCount != 2 {
		t.Fatalf("statement ran despite the pause: %+v", r)
	}
}
