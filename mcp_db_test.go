package main

// End-to-end MCP tests: a real App over a real database, driven the way an MCP
// client drives it — JSON-RPC in, JSON-RPC out. The package tests cover the
// protocol; these cover the adapter, which is where a catalog call can be wired to
// the wrong argument and no unit test would notice.

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// mcpCall drives one JSON-RPC request through the app's stdio handler.
func mcpCall(t *testing.T, a *App, method string, params interface{}) map[string]interface{} {
	t.Helper()
	body := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out bytes.Buffer
	if err := a.RunMCPStdio(bytes.NewReader(append(raw, '\n')), &out); err != nil {
		t.Fatalf("stdio: %v", err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	return resp
}

func mcpToolText(t *testing.T, a *App, tool string, args map[string]interface{}) (string, bool) {
	t.Helper()
	resp := mcpCall(t, a, "tools/call", map[string]interface{}{"name": tool, "arguments": args})
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("no result in %v", resp)
	}
	isErr, _ := result["isError"].(bool)
	content := result["content"].([]interface{})[0].(map[string]interface{})
	return content["text"].(string), isErr
}

func TestMCPListsTheRealCatalog(t *testing.T) {
	conn := sqliteDB(t, "mcp1", "scratch")
	createPartsTable(t, conn)
	a := searchApp(conn)

	text, isErr := mcpToolText(t, a, "list_connections", nil)
	if isErr {
		t.Fatalf("list_connections: %s", text)
	}
	if !strings.Contains(text, "mcp1") {
		t.Fatalf("the connection is missing from %q", text)
	}

	text, isErr = mcpToolText(t, a, "list_tables", map[string]interface{}{"connection": "mcp1"})
	if isErr {
		t.Fatalf("list_tables: %s", text)
	}
	if !strings.Contains(text, "parts") {
		t.Fatalf("the table is missing from %q", text)
	}

	text, isErr = mcpToolText(t, a, "describe_table", map[string]interface{}{
		"connection": "mcp1", "table": "parts",
	})
	if isErr {
		t.Fatalf("describe_table: %s", text)
	}
	if !strings.Contains(text, `"isPrimaryKey": true`) {
		t.Fatalf("the primary key is missing from %q", text)
	}
}

// A tool query runs through the app's own statement path, so it sees what the UI
// sees — including the row cap.
func TestMCPQueryRunsAgainstTheDatabase(t *testing.T) {
	conn := sqliteDB(t, "mcp2", "scratch")
	createPartsTable(t, conn)
	if _, err := conn.DB.Exec(`INSERT INTO parts (id, name) VALUES (1, 'gear')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := searchApp(conn)

	text, isErr := mcpToolText(t, a, "query", map[string]interface{}{
		"connection": "mcp2",
		"sql":        "SELECT id, name FROM parts",
	})
	if isErr {
		t.Fatalf("query: %s", text)
	}
	if !strings.Contains(text, "gear") {
		t.Fatalf("the row is missing from %q", text)
	}
}

// The write tool must not exist by default, and when it does it must be confirmed.
func TestMCPWritePathIsOptIn(t *testing.T) {
	conn := sqliteDB(t, "mcp3", "scratch")
	createPartsTable(t, conn)
	a := searchApp(conn)

	// Default settings: no execute tool.
	resp := mcpCall(t, a, "tools/list", nil)
	tools := resp["result"].(map[string]interface{})["tools"].([]interface{})
	for _, raw := range tools {
		if raw.(map[string]interface{})["name"] == "execute" {
			t.Fatal("execute must not be advertised by default")
		}
	}

	// Turning the setting on is still not enough without a confirmer.
	settings := a.GetSettings()
	settings.MCPAllowWrites = true
	a.settings = settings
	resp = mcpCall(t, a, "tools/list", nil)
	tools = resp["result"].(map[string]interface{})["tools"].([]interface{})
	for _, raw := range tools {
		if raw.(map[string]interface{})["name"] == "execute" {
			t.Fatal("execute must not appear without a confirmer")
		}
	}

	// With a confirmer that refuses, the statement is refused and does not run.
	asked := 0
	a.SetMCPConfirmer(func(connName, sql string) bool {
		asked++
		return false
	})
	text, isErr := mcpToolText(t, a, "execute", map[string]interface{}{
		"connection": "mcp3",
		"sql":        "INSERT INTO parts (id, name) VALUES (9, 'nope')",
	})
	if !isErr {
		t.Fatalf("a refused write must be an error, got %q", text)
	}
	if asked != 1 {
		t.Fatalf("confirm asked %d times", asked)
	}
	var count int
	if err := conn.DB.QueryRow(`SELECT count(*) FROM parts`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("a refused write reached the database: %d rows", count)
	}

	// With one that approves, it runs.
	a.SetMCPConfirmer(func(string, string) bool { return true })
	text, isErr = mcpToolText(t, a, "execute", map[string]interface{}{
		"connection": "mcp3",
		"sql":        "INSERT INTO parts (id, name) VALUES (9, 'yes')",
	})
	if isErr {
		t.Fatalf("approved write failed: %s", text)
	}
	if err := conn.DB.QueryRow(`SELECT count(*) FROM parts`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("the approved write did not land: %d rows", count)
	}

	// Dropping the confirmer returns the server to read-only.
	a.SetMCPConfirmer(nil)
	resp = mcpCall(t, a, "tools/list", nil)
	tools = resp["result"].(map[string]interface{})["tools"].([]interface{})
	for _, raw := range tools {
		if raw.(map[string]interface{})["name"] == "execute" {
			t.Fatal("execute must disappear when the confirmer is removed")
		}
	}
}

// The stdio transport writes one JSON object per line and nothing else, which is
// what an MCP client parses.
func TestMCPStdioFraming(t *testing.T) {
	conn := sqliteDB(t, "mcp4", "scratch")
	a := searchApp(conn)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := a.RunMCPStdio(strings.NewReader(input), &out); err != nil {
		t.Fatalf("stdio: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// The notification produces no line: two requests, two responses.
	if len(lines) != 2 {
		t.Fatalf("want 2 response lines, got %d: %q", len(lines), out.String())
	}
	for i, line := range lines {
		var resp map[string]interface{}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("line %d is not JSON (%q): %v", i, line, err)
		}
		if resp["jsonrpc"] != "2.0" {
			t.Fatalf("line %d is not a JSON-RPC 2.0 message: %q", i, line)
		}
	}
}

// The HTTP endpoint is the same handler, so an MCP client can attach to a running
// app. A notification answers 202.
func TestMCPOverHTTP(t *testing.T) {
	conn := sqliteDB(t, "mcp5", "scratch")
	a := searchApp(conn)
	mux := a.routes()

	req := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "list_connections") {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// A notification has no answer: 202 with an empty body.
	note := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, note)
	if rec.Code != 202 {
		t.Fatalf("notification status = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("a notification must not have a body: %q", rec.Body.String())
	}
}
