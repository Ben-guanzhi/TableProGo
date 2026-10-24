// Package mcp exposes a database client's catalog and a read-only query surface
// over the Model Context Protocol, so an AI client (Claude Desktop and friends)
// can look at the schema and run SELECTs without being given a write path.
//
// Transport-agnostic on purpose, the way the protocol is meant to be: Handle takes
// one JSON-RPC 2.0 request and returns one response, so stdio and HTTP share the
// same tool implementations. This package holds the protocol and the tool
// dispatch; what a tool does to a database stays behind Catalog.
//
// Security posture: read-only is the default, and a write tool exists only when the
// caller supplies a confirm hook. Nothing here trusts the model's SQL — the catalog
// and the row limit are enforced on this side.
package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Catalog is what the tools need from the client. Keeping it this small is what
// lets the package be tested without a database and reused by another client.
type Catalog interface {
	// Connections lists the connected databases: an id, a display name and the
	// engine type.
	Connections() []ConnectionInfo
	// Schemas lists the database/schema names of one connection.
	Schemas(connID string) ([]string, error)
	// Tables lists objects of one schema. An empty schema means the engine's
	// default.
	Tables(connID, database, schema string) ([]TableInfo, error)
	// Columns describes one table.
	Columns(connID, database, schema, table string) ([]ColumnInfo, error)
	// Query runs one statement and returns its columns and rows. The boolean
	// reports whether the statement was a read: a write is only reached when the
	// server was built with a confirm hook.
	Query(connID, database, sql string, limit int) (*QueryResult, error)
}

// ConnectionInfo is one connected database as the tool reports it.
type ConnectionInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	DBName string `json:"database"`
}

// TableInfo is one object of a schema.
type TableInfo struct {
	Name    string `json:"name"`
	Schema  string `json:"schema"`
	Type    string `json:"type"`
	Comment string `json:"comment,omitempty"`
}

// ColumnInfo describes one column.
type ColumnInfo struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	DefaultValue string `json:"defaultValue,omitempty"`
	IsPrimaryKey bool   `json:"isPrimaryKey"`
	Comment      string `json:"comment,omitempty"`
}

// QueryResult is one statement's outcome.
type QueryResult struct {
	Columns   []string        `json:"columns"`
	Rows      [][]interface{} `json:"rows"`
	RowCount  int             `json:"rowCount"`
	Truncated bool            `json:"truncated"`
}

// Level is what a client is allowed to do. It is a property of the *server*, not
// of a request: a model cannot ask for a write and get one.
type Level int

const (
	// ReadOnly is the default: catalog tools plus SELECT.
	ReadOnly Level = iota
	// ReadWrite additionally allows DML through a confirming hook.
	ReadWrite
)

// Options configure the server.
type Options struct {
	// Level is the capability the server exposes. Zero value is ReadOnly.
	Level Level
	// MaxRows caps how many rows a query returns; 0 means DefaultMaxRows.
	MaxRows int
	// Confirm is asked before any statement that is not a read. Without it the
	// server stays read-only whatever Level says — a write path that nobody
	// approves is a write path that runs unattended.
	Confirm func(connID, sql string) bool
	// ServerName and ServerVersion identify the server in `initialize`.
	ServerName    string
	ServerVersion string
}

// DefaultMaxRows keeps one tool call from dumping a whole table into a context
// window.
const DefaultMaxRows = 200

// Server answers MCP requests.
type Server struct {
	catalog Catalog
	opts    Options

	mu sync.Mutex
	// initialized tracks the handshake so tools/list before initialize is refused
	// rather than answered with a half-built server.
	initialized bool
}

// New returns a server over catalog.
func New(catalog Catalog, opts Options) *Server {
	if opts.MaxRows <= 0 {
		opts.MaxRows = DefaultMaxRows
	}
	if opts.ServerName == "" {
		opts.ServerName = "tableprogo"
	}
	if opts.ServerVersion == "" {
		opts.ServerVersion = "0.1.0"
	}
	return &Server{catalog: catalog, opts: opts}
}

// ---------- JSON-RPC 2.0 ----------

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func errResponse(id json.RawMessage, code int, message string) *response {
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

// Handle processes one JSON-RPC request. A notification (no id) returns nil: the
// protocol says not to answer those.
func (s *Server) Handle(raw []byte) []byte {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		out, _ := json.Marshal(errResponse(nil, -32700, "parse error: "+err.Error()))
		return out
	}
	resp := s.handle(&req)
	if resp == nil {
		return nil
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil
	}
	return out
}

func (s *Server) handle(req *request) *response {
	switch req.Method {
	case "initialize":
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()
		return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo": map[string]interface{}{
				"name":    s.opts.ServerName,
				"version": s.opts.ServerVersion,
			},
		}}
	case "notifications/initialized", "initialized", "ping", "cancel":
		if req.Method == "ping" {
			return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}
		}
		return nil
	case "tools/list":
		return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{"tools": s.tools()}}
	case "tools/call":
		return s.callTool(req)
	default:
		if len(req.ID) == 0 {
			return nil
		}
		return errResponse(req.ID, -32601, "method not found: "+req.Method)
	}
}

// ---------- tools ----------

type tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type param struct {
	Type     string `json:"type"`
	Desc     string `json:"description"`
	Required bool   `json:"-"`
}

func schemaOf(params map[string]param) map[string]interface{} {
	props := map[string]interface{}{}
	var required []string
	for name, p := range params {
		props[name] = map[string]interface{}{"type": p.Type, "description": p.Desc}
		if p.Required {
			required = append(required, name)
		}
	}
	out := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// ToolDefinition is one advertised tool. It is exported so a client that offers
// the same tools to a chat model (see internal/llm) cannot drift from what this
// server advertises.
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// ToolsForLLM returns the advertised tools in the exported shape.
func (s *Server) ToolsForLLM() []ToolDefinition {
	list := s.tools()
	out := make([]ToolDefinition, 0, len(list))
	for _, t := range list {
		out = append(out, ToolDefinition{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// tools is the advertised tool list. The write tool appears only when the server
// actually has a confirming hook, so a client never sees a capability it cannot
// use.
func (s *Server) tools() []tool {
	list := []tool{
		{
			Name:        "list_connections",
			Description: "列出已连接的数据库（id、名称、引擎类型）。其他工具都用这里的 connection id。",
			InputSchema: schemaOf(map[string]param{}),
		},
		{
			Name:        "list_schemas",
			Description: "列出某连接下的数据库/schema 名称。",
			InputSchema: schemaOf(map[string]param{
				"connection": {Type: "string", Desc: "连接 id", Required: true},
			}),
		},
		{
			Name:        "list_tables",
			Description: "列出某连接某 schema 下的表与视图。",
			InputSchema: schemaOf(map[string]param{
				"connection": {Type: "string", Desc: "连接 id", Required: true},
				"database":   {Type: "string", Desc: "数据库名，留空用连接默认库"},
				"schema":     {Type: "string", Desc: "schema 名，留空用引擎默认"},
			}),
		},
		{
			Name:        "describe_table",
			Description: "返回一张表的列结构（名称/类型/可空/主键/默认值/注释）。",
			InputSchema: schemaOf(map[string]param{
				"connection": {Type: "string", Desc: "连接 id", Required: true},
				"table":      {Type: "string", Desc: "表名", Required: true},
				"database":   {Type: "string", Desc: "数据库名，留空用连接默认库"},
				"schema":     {Type: "string", Desc: "schema 名，留空用引擎默认"},
			}),
		},
		{
			Name:        "query",
			Description: fmt.Sprintf("执行只读 SQL（SELECT/WITH/SHOW/DESCRIBE/EXPLAIN），最多返回 %d 行。", s.opts.MaxRows),
			InputSchema: schemaOf(map[string]param{
				"connection": {Type: "string", Desc: "连接 id", Required: true},
				"sql":        {Type: "string", Desc: "只读 SQL", Required: true},
				"database":   {Type: "string", Desc: "数据库名，留空用连接默认库"},
			}),
		},
	}
	if s.writeAllowed() {
		list = append(list, tool{
			Name:        "execute",
			Description: "执行一条写语句（INSERT/UPDATE/DELETE 或 DDL）。每次调用都会请求用户确认。",
			InputSchema: schemaOf(map[string]param{
				"connection": {Type: "string", Desc: "连接 id", Required: true},
				"sql":        {Type: "string", Desc: "待执行的 SQL", Required: true},
				"database":   {Type: "string", Desc: "数据库名，留空用连接默认库"},
			}),
		})
	}
	return list
}

// writeAllowed reports whether a write tool is offered at all.
func (s *Server) writeAllowed() bool {
	return s.opts.Level >= ReadWrite && s.opts.Confirm != nil
}

type callParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

func (s *Server) callTool(req *request) *response {
	var params callParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResponse(req.ID, -32602, "invalid params: "+err.Error())
	}
	content, err := s.dispatch(params.Name, params.Arguments)
	if err != nil {
		// A tool failure is reported inside the result (isError), not as a
		// protocol error: the model should see the message and can retry.
		return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": err.Error()}},
			"isError": true,
		}}
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{
		"content": []map[string]interface{}{{"type": "text", "text": content}},
	}}
}

func (s *Server) dispatch(name string, args map[string]interface{}) (string, error) {
	arg := func(key string) string {
		if v, ok := args[key].(string); ok {
			return v
		}
		return ""
	}
	switch name {
	case "list_connections":
		return s.listConnections()
	case "list_schemas":
		return s.listSchemas(arg("connection"))
	case "list_tables":
		return s.listTables(arg("connection"), arg("database"), arg("schema"))
	case "describe_table":
		return s.describeTable(arg("connection"), arg("database"), arg("schema"), arg("table"))
	case "query":
		return s.query(arg("connection"), arg("database"), arg("sql"))
	case "execute":
		if !s.writeAllowed() {
			return "", fmt.Errorf("这个服务是只读的：execute 不可用")
		}
		return s.execute(arg("connection"), arg("database"), arg("sql"))
	default:
		return "", fmt.Errorf("未知工具: %s", name)
	}
}

func (s *Server) listConnections() (string, error) {
	conns := s.catalog.Connections()
	if len(conns) == 0 {
		return "没有已连接的数据库。请在客户端里连接一个数据库后重试。", nil
	}
	return marshalJSON(conns)
}

func (s *Server) listSchemas(connID string) (string, error) {
	if connID == "" {
		return "", fmt.Errorf("缺少参数 connection")
	}
	schemas, err := s.catalog.Schemas(connID)
	if err != nil {
		return "", err
	}
	return marshalJSON(schemas)
}

func (s *Server) listTables(connID, database, schema string) (string, error) {
	if connID == "" {
		return "", fmt.Errorf("缺少参数 connection")
	}
	tables, err := s.catalog.Tables(connID, database, schema)
	if err != nil {
		return "", err
	}
	return marshalJSON(tables)
}

func (s *Server) describeTable(connID, database, schema, table string) (string, error) {
	if connID == "" {
		return "", fmt.Errorf("缺少参数 connection")
	}
	if table == "" {
		return "", fmt.Errorf("缺少参数 table")
	}
	columns, err := s.catalog.Columns(connID, database, schema, table)
	if err != nil {
		return "", err
	}
	return marshalJSON(columns)
}

// readOnlyPrefixes are the statement kinds the query tool accepts. The check is on
// the first keyword after comments, so a value like "select" inside a string cannot
// turn an UPDATE into a read.
var readOnlyPrefixes = []string{"SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "PRAGMA", "TABLE", "VALUES"}

// IsReadStatement reports whether sql only reads. It is exported so the client can
// reuse the same rule for its own write guard.
func IsReadStatement(sql string) bool {
	trimmed := stripLeadingCommentsAndSpace(sql)
	upper := strings.ToUpper(trimmed)
	for _, prefix := range readOnlyPrefixes {
		if strings.HasPrefix(upper, prefix) {
			// "SELECT" must be a whole word: "SELECTED" is not a read statement.
			if len(upper) == len(prefix) || !isWordByte(upper[len(prefix)]) {
				return true
			}
		}
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// stripLeadingCommentsAndSpace skips whitespace, -- comments and /* */ blocks so
// the first real keyword is what the classification sees.
func stripLeadingCommentsAndSpace(sql string) string {
	text := sql
	for {
		text = strings.TrimLeft(text, " \t\r\n;")
		switch {
		case strings.HasPrefix(text, "--"):
			if nl := strings.IndexByte(text, '\n'); nl >= 0 {
				text = text[nl+1:]
				continue
			}
			return ""
		case strings.HasPrefix(text, "/*"):
			if end := strings.Index(text, "*/"); end >= 0 {
				text = text[end+2:]
				continue
			}
			return ""
		default:
			return text
		}
	}
}

func (s *Server) query(connID, database, sql string) (string, error) {
	if connID == "" {
		return "", fmt.Errorf("缺少参数 connection")
	}
	if strings.TrimSpace(sql) == "" {
		return "", fmt.Errorf("缺少参数 sql")
	}
	if !IsReadStatement(sql) {
		return "", fmt.Errorf("query 只接受只读语句（SELECT/WITH/SHOW/DESCRIBE/EXPLAIN）；写操作请用 execute")
	}
	result, err := s.catalog.Query(connID, database, sql, s.opts.MaxRows)
	if err != nil {
		return "", err
	}
	return marshalJSON(result)
}

func (s *Server) execute(connID, database, sql string) (string, error) {
	if connID == "" {
		return "", fmt.Errorf("缺少参数 connection")
	}
	if strings.TrimSpace(sql) == "" {
		return "", fmt.Errorf("缺少参数 sql")
	}
	// A statement that reads is not a write; refusing it here would make the tool
	// surprising. It goes through the same read path (no confirmation needed).
	if IsReadStatement(sql) {
		return s.query(connID, database, sql)
	}
	if !s.opts.Confirm(connID, sql) {
		return "", fmt.Errorf("用户拒绝了这条语句：%s", firstLine(sql))
	}
	result, err := s.catalog.Query(connID, database, sql, s.opts.MaxRows)
	if err != nil {
		return "", err
	}
	return marshalJSON(result)
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 120 {
		text = text[:120] + "…"
	}
	return text
}

func marshalJSON(v interface{}) (string, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
