package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// fakeCatalog is what the tools talk to. The point of the Catalog interface is that
// this is all a test needs.
type fakeCatalog struct {
	conns    []ConnectionInfo
	schemas  []string
	tables   []TableInfo
	columns  []ColumnInfo
	result   *QueryResult
	err      error
	queries  []string
	limitSet int
}

func (f *fakeCatalog) Connections() []ConnectionInfo { return f.conns }
func (f *fakeCatalog) Schemas(string) ([]string, error) {
	return f.schemas, f.err
}
func (f *fakeCatalog) Tables(_, _, _ string) ([]TableInfo, error) { return f.tables, f.err }
func (f *fakeCatalog) Columns(_, _, _, _ string) ([]ColumnInfo, error) {
	return f.columns, f.err
}
func (f *fakeCatalog) Query(_, _, sql string, limit int) (*QueryResult, error) {
	f.queries = append(f.queries, sql)
	f.limitSet = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func newTestServer(opts Options) (*Server, *fakeCatalog) {
	cat := &fakeCatalog{
		conns:   []ConnectionInfo{{ID: "c1", Name: "app", Type: "postgresql", DBName: "app"}},
		schemas: []string{"public"},
		tables:  []TableInfo{{Name: "orders", Schema: "public", Type: "table"}},
		columns: []ColumnInfo{{Name: "id", Type: "bigint", IsPrimaryKey: true}},
		result:  &QueryResult{Columns: []string{"id"}, Rows: [][]interface{}{{1}}, RowCount: 1},
	}
	return New(cat, opts), cat
}

// rpc drives one request through Handle and decodes the response.
func rpc(t *testing.T, s *Server, method string, params interface{}) map[string]interface{} {
	t.Helper()
	body := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := s.Handle(raw)
	if out == nil {
		return nil
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return resp
}

// callResult drives a tools/call and returns the text the model would see.
func callResult(t *testing.T, s *Server, tool string, args map[string]interface{}) (string, bool) {
	t.Helper()
	resp := rpc(t, s, "tools/call", map[string]interface{}{"name": tool, "arguments": args})
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("no result in %v", resp)
	}
	isError, _ := result["isError"].(bool)
	content := result["content"].([]interface{})[0].(map[string]interface{})
	return content["text"].(string), isError
}

func TestInitializeReportsToolsCapability(t *testing.T) {
	s, _ := newTestServer(Options{})
	resp := rpc(t, s, "initialize", map[string]interface{}{})
	result := resp["result"].(map[string]interface{})
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	if _, ok := result["capabilities"].(map[string]interface{})["tools"]; !ok {
		t.Fatal("the server must advertise the tools capability")
	}
}

// A notification has no id and must not be answered.
func TestNotificationGetsNoResponse(t *testing.T) {
	s, _ := newTestServer(Options{})
	raw := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if out := s.Handle(raw); out != nil {
		t.Fatalf("a notification must not be answered, got %s", out)
	}
}

func TestToolsListIsReadOnlyByDefault(t *testing.T) {
	s, _ := newTestServer(Options{})
	resp := rpc(t, s, "tools/list", nil)
	tools := resp["result"].(map[string]interface{})["tools"].([]interface{})
	names := map[string]bool{}
	for _, raw := range tools {
		names[raw.(map[string]interface{})["name"].(string)] = true
	}
	for _, want := range []string{"list_connections", "list_schemas", "list_tables", "describe_table", "query"} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}
	// The write tool must not be advertised when the server cannot confirm it.
	if names["execute"] {
		t.Error("a read-only server must not advertise execute")
	}
}

// A level alone is not enough: without a confirm hook there is still no write path.
func TestExecuteNeedsBothLevelAndConfirm(t *testing.T) {
	s, _ := newTestServer(Options{Level: ReadWrite})
	resp := rpc(t, s, "tools/list", nil)
	tools := resp["result"].(map[string]interface{})["tools"].([]interface{})
	for _, raw := range tools {
		if raw.(map[string]interface{})["name"] == "execute" {
			t.Fatal("execute must not appear without a confirm hook")
		}
	}
}

func TestExecuteAppearsWithConfirmAndRespectsIt(t *testing.T) {
	asked := 0
	approved := true
	s, cat := newTestServer(Options{
		Level: ReadWrite,
		Confirm: func(connID, sql string) bool {
			asked++
			return approved
		},
	})

	text, isErr := callResult(t, s, "execute", map[string]interface{}{
		"connection": "c1",
		"sql":        "UPDATE orders SET x = 1",
	})
	if isErr {
		t.Fatalf("approved execute failed: %s", text)
	}
	if asked != 1 {
		t.Fatalf("confirm asked %d times", asked)
	}
	if len(cat.queries) != 1 {
		t.Fatalf("queries = %v", cat.queries)
	}

	approved = false
	text, isErr = callResult(t, s, "execute", map[string]interface{}{
		"connection": "c1",
		"sql":        "DELETE FROM orders",
	})
	if !isErr {
		t.Fatal("a declined statement must be an error")
	}
	if !strings.Contains(text, "用户拒绝") {
		t.Fatalf("text = %q", text)
	}
	// Nothing extra ran.
	if len(cat.queries) != 1 {
		t.Fatalf("a declined statement reached the database: %v", cat.queries)
	}
}

// execute on a read statement is not a write: it runs through the read path without
// asking the user.
func TestExecuteOnReadDoesNotAsk(t *testing.T) {
	asked := 0
	s, _ := newTestServer(Options{
		Level:   ReadWrite,
		Confirm: func(string, string) bool { asked++; return true },
	})
	text, isErr := callResult(t, s, "execute", map[string]interface{}{
		"connection": "c1",
		"sql":        "SELECT 1",
	})
	if isErr {
		t.Fatalf("read through execute failed: %s", text)
	}
	if asked != 0 {
		t.Fatalf("a read must not prompt, asked %d times", asked)
	}
}

// The query tool refuses writes outright, whatever the level.
func TestQueryRefusesWrites(t *testing.T) {
	s, cat := newTestServer(Options{Level: ReadWrite, Confirm: func(string, string) bool { return true }})
	text, isErr := callResult(t, s, "query", map[string]interface{}{
		"connection": "c1",
		"sql":        "DELETE FROM orders",
	})
	if !isErr {
		t.Fatal("query must refuse a write")
	}
	if !strings.Contains(text, "只读") {
		t.Fatalf("text = %q", text)
	}
	if len(cat.queries) != 0 {
		t.Fatalf("the write reached the database: %v", cat.queries)
	}
}

func TestQueryPassesTheRowLimit(t *testing.T) {
	s, cat := newTestServer(Options{MaxRows: 77})
	if _, isErr := callResult(t, s, "query", map[string]interface{}{
		"connection": "c1",
		"sql":        "SELECT * FROM orders",
	}); isErr {
		t.Fatal("query failed")
	}
	if cat.limitSet != 77 {
		t.Fatalf("limit = %d, want 77", cat.limitSet)
	}
}

func TestCatalogToolsReturnTheCatalog(t *testing.T) {
	s, _ := newTestServer(Options{})

	text, isErr := callResult(t, s, "list_connections", nil)
	if isErr || !strings.Contains(text, `"app"`) {
		t.Fatalf("list_connections = %q (err %v)", text, isErr)
	}
	text, isErr = callResult(t, s, "list_schemas", map[string]interface{}{"connection": "c1"})
	if isErr || !strings.Contains(text, `"public"`) {
		t.Fatalf("list_schemas = %q", text)
	}
	text, isErr = callResult(t, s, "list_tables", map[string]interface{}{"connection": "c1"})
	if isErr || !strings.Contains(text, `"orders"`) {
		t.Fatalf("list_tables = %q", text)
	}
	text, isErr = callResult(t, s, "describe_table", map[string]interface{}{
		"connection": "c1", "table": "orders",
	})
	if isErr || !strings.Contains(text, `"isPrimaryKey": true`) {
		t.Fatalf("describe_table = %q", text)
	}
}

func TestMissingArgumentsAreErrors(t *testing.T) {
	s, _ := newTestServer(Options{})
	for _, tc := range []struct {
		tool string
		args map[string]interface{}
		want string
	}{
		{"list_schemas", map[string]interface{}{}, "connection"},
		{"list_tables", map[string]interface{}{}, "connection"},
		{"describe_table", map[string]interface{}{"connection": "c1"}, "table"},
		{"query", map[string]interface{}{"connection": "c1"}, "sql"},
	} {
		text, isErr := callResult(t, s, tc.tool, tc.args)
		if !isErr {
			t.Errorf("%s without %s must fail", tc.tool, tc.want)
			continue
		}
		if !strings.Contains(text, tc.want) {
			t.Errorf("%s error %q does not mention %s", tc.tool, text, tc.want)
		}
	}
}

func TestCatalogErrorSurfaces(t *testing.T) {
	s, cat := newTestServer(Options{})
	cat.err = fmt.Errorf("connection is closed")
	text, isErr := callResult(t, s, "list_tables", map[string]interface{}{"connection": "c1"})
	if !isErr || !strings.Contains(text, "connection is closed") {
		t.Fatalf("text = %q (err %v)", text, isErr)
	}
}

func TestUnknownMethodAndTool(t *testing.T) {
	s, _ := newTestServer(Options{})
	resp := rpc(t, s, "does/not/exist", nil)
	if resp["error"] == nil {
		t.Fatal("an unknown method must be a protocol error")
	}
	text, isErr := callResult(t, s, "drop_everything", nil)
	if !isErr || !strings.Contains(text, "未知工具") {
		t.Fatalf("text = %q", text)
	}
}

// The read/write classification must not be fooled by comments or by a keyword that
// only looks like a prefix.
func TestIsReadStatement(t *testing.T) {
	reads := []string{
		"SELECT 1",
		"  select * from t",
		"-- leading comment\nSELECT 1",
		"/* block */ SELECT 1",
		"WITH x AS (SELECT 1) SELECT * FROM x",
		"SHOW TABLES",
		"DESCRIBE orders",
		"EXPLAIN SELECT 1",
		"PRAGMA table_info(orders)",
		"TABLE orders",
	}
	for _, sql := range reads {
		if !IsReadStatement(sql) {
			t.Errorf("%q should be a read", sql)
		}
	}
	writes := []string{
		"UPDATE orders SET x = 1",
		"DELETE FROM orders",
		"INSERT INTO orders VALUES (1)",
		"DROP TABLE orders",
		"SELECTED rows", // not the SELECT keyword
		"  /* c */ DELETE FROM t",
		"",
	}
	for _, sql := range writes {
		if IsReadStatement(sql) {
			t.Errorf("%q must not be classified as a read", sql)
		}
	}
}

// A tool failure is reported inside the result rather than as a protocol error, so
// the model sees the message.
func TestToolFailureIsAnInResultError(t *testing.T) {
	s, cat := newTestServer(Options{})
	cat.err = fmt.Errorf("boom")
	resp := rpc(t, s, "tools/call", map[string]interface{}{
		"name":      "list_schemas",
		"arguments": map[string]interface{}{"connection": "c1"},
	})
	if resp["error"] != nil {
		t.Fatalf("a tool failure must not be a protocol error: %v", resp["error"])
	}
	result := resp["result"].(map[string]interface{})
	if result["isError"] != true {
		t.Fatalf("isError = %v", result["isError"])
	}
}

func TestPingAnswers(t *testing.T) {
	s, _ := newTestServer(Options{})
	resp := rpc(t, s, "ping", nil)
	if resp == nil || resp["result"] == nil {
		t.Fatalf("ping = %v", resp)
	}
}

func TestParseError(t *testing.T) {
	s, _ := newTestServer(Options{})
	out := s.Handle([]byte(`{not json`))
	var resp map[string]interface{}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] == nil {
		t.Fatal("a parse error must be reported")
	}
}
