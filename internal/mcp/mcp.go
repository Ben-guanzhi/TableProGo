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
	"strconv"
	"strings"
	"sync"

	"github.com/dbcore/dbcore/masking"

	"tablepro-go/internal/mcpcompact"
)

// isIdentifierLike reports whether s is a plain SQL identifier: letters, digits
// (not leading) and underscores. The sample_table tool builds its SQL from the
// table name in-process, so only identifier-like names are accepted — quoting
// is dialect-specific and the Catalog interface deliberately does not expose
// the engine spelling to this package.
func isIdentifierLike(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

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
	// Masked reports that string cells went through data masking (sensitive
	// column names and PII shapes) before leaving the machine. A client that
	// copies masked values back to the database copies stars, which is safe.
	Masked bool `json:"masked,omitempty"`
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

// errNotInitialized is the JSON-RPC code the MCP spec assigns to a tool call
// that skips the handshake.
const errNotInitialized = -32002

// ready reports the handshake state. The comment on `initialized` promised this
// refusal; without the check the field was write-only.
func (s *Server) ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized
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
		if !s.ready() {
			return errResponse(req.ID, errNotInitialized, "Server not initialized")
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{"tools": s.tools()}}
	case "tools/call":
		if !s.ready() {
			return errResponse(req.ID, errNotInitialized, "Server not initialized")
		}
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
			Name:        "sample_table",
			Description: fmt.Sprintf("只读抽样某表的前 N 行（limit 上限 %d，不传默认 20），行内容经数据掩码后返回，供模型快速了解数据形态。format=compact 时返回制表符分隔的紧凑文本（比 JSON 省很多 token）。", DefaultMaxRows),
			InputSchema: schemaOf(map[string]param{
				"connection": {Type: "string", Desc: "连接 id", Required: true},
				"table":      {Type: "string", Desc: "表名", Required: true},
				"database":   {Type: "string", Desc: "数据库名，留空用连接默认库"},
				"schema":     {Type: "string", Desc: "schema 名，留空用引擎默认"},
				"limit":      {Type: "number", Desc: "行数上限 (1-" + strconv.Itoa(DefaultMaxRows) + ", 默认 20)"},
				"format":     {Type: "string", Desc: "输出格式: json（默认）或 compact（制表符分隔，省 token）"},
			}),
		},
		{
			Name:        "query",
			Description: fmt.Sprintf("执行只读 SQL（SELECT/WITH/SHOW/DESCRIBE/EXPLAIN），最多返回 %d 行。format=compact 时返回制表符分隔的紧凑文本。", s.opts.MaxRows),
			InputSchema: schemaOf(map[string]param{
				"connection": {Type: "string", Desc: "连接 id", Required: true},
				"sql":        {Type: "string", Desc: "只读 SQL", Required: true},
				"database":   {Type: "string", Desc: "数据库名，留空用连接默认库"},
				"format":     {Type: "string", Desc: "输出格式: json（默认）或 compact（制表符分隔，省 token）"},
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
	case "sample_table":
		return s.sampleTable(arg("connection"), arg("database"), arg("schema"), arg("table"), args["limit"], arg("format"))
	case "query":
		return s.query(arg("connection"), arg("database"), arg("sql"), arg("format"))
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

// sampleTable 只读抽样一张表的前 N 行。SQL 在服务端拼装: 表名只允许
// identifier-like 字符 (方言引用不可用, Catalog 不暴露引擎拼写; 非法名直接
// 拒绝), LIMIT 由本函数钳制 —— 模型无法通过 limit 参数把上下文撑爆, 也无法
// 借表名注入第二语句 (工具层本就只接单语句)。行内容出机器前经数据掩码。
func (s *Server) sampleTable(connID, database, schema, table string, limitArg interface{}, format string) (string, error) {
	if connID == "" {
		return "", fmt.Errorf("缺少参数 connection")
	}
	if table == "" {
		return "", fmt.Errorf("缺少参数 table")
	}
	limit := 20
	switch v := limitArg.(type) {
	case float64: // JSON number
		limit = int(v)
	case string:
		if strings.TrimSpace(v) != "" {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n <= 0 {
				return "", fmt.Errorf("limit 必须是正整数, got %q", v)
			}
			limit = n
		}
	case nil:
	default:
		return "", fmt.Errorf("limit 必须是数字")
	}
	if limit <= 0 {
		return "", fmt.Errorf("limit 必须是正整数")
	}
	// 上限与服务器的行预算一致 (MaxRows 未配置时回落包默认): 模型不能借
	// limit 参数撑爆上下文, 也不该比 query 工具拿得更多。
	if max := s.opts.MaxRows; max > 0 && limit > max {
		limit = max
	} else if max <= 0 && limit > DefaultMaxRows {
		limit = DefaultMaxRows
	}
	// 表名只允许 identifier-like 字符, 不满足直接拒绝, 杜绝注入面。
	if !isIdentifierLike(table) {
		return "", fmt.Errorf("sample_table 的表名只允许字母/数字/下划线: %q", table)
	}
	// 先探表是否存在 (不存在的表在 Columns 报错, 信息明确)。
	if _, err := s.catalog.Columns(connID, database, schema, table); err != nil {
		return "", err
	}
	sql := "SELECT * FROM " + table + " LIMIT " + strconv.Itoa(limit)
	result, err := s.catalog.Query(connID, database, sql, limit)
	if err != nil {
		return "", err
	}
	return renderResult(MaskQueryResult(result), format)
}

// renderResult formats a masked result for the wire. json is the historical
// shape; compact is the same information as tab-separated text (see
// internal/mcpcompact for why and for the exact grammar).
func renderResult(result *QueryResult, format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "json":
		return marshalJSON(result)
	case "compact":
		if result == nil {
			return "", nil
		}
		return mcpcompact.Render(mcpcompact.Result{
			Columns:   result.Columns,
			Rows:      result.Rows,
			Truncated: result.Truncated,
			Masked:    result.Masked,
		}), nil
	}
	return "", fmt.Errorf("format 只接受 json 或 compact, got %q", format)
}

// maskPolicy 是 MCP/LLM 输出共用的数据掩码策略 (dbcore/masking 默认档)。
var maskPolicy = masking.Default()

// MaskQueryResult 掩码一个结果集的字符串单元格 (导出供 LLM 助手路径复用,
// 与 MCP 工具共用同一策略)。列名来自结果自身, 所以掩码与结果集严格对齐,
// 不依赖调用方记忆列序。
func MaskQueryResult(result *QueryResult) *QueryResult {
	if result == nil || len(result.Rows) == 0 {
		return result
	}
	for i, row := range result.Rows {
		result.Rows[i] = maskPolicy.RowInterface(result.Columns, row)
	}
	result.Masked = true
	return result
}

// readOnlyPrefixes are the statement kinds the query tool accepts. The check is on
// the first keyword after comments, so a value like "select" inside a string cannot
// turn an UPDATE into a read.
var readOnlyPrefixes = []string{"SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "PRAGMA", "TABLE", "VALUES"}

// isSingleStatement reports whether sql carries exactly one statement: top-level
// semicolons are counted by a scanner that skips string literals, quoted
// identifiers, comments and dollar quotes — the constructs that legally contain
// a ';'. A multi-statement string must be refused outright, not classified by
// its first keyword: `SELECT 1; DROP TABLE x` starts with SELECT, so the
// first-keyword check alone would call it a read and hand the whole string to
// the driver — several engines (go-mssqldb batch, PostgreSQL simple protocol)
// run both halves, which turns the read-only gate into a write path. This is
// the same scanner shape the editor's script runner (sqlscript.Split) uses.
func isSingleStatement(sql string) bool {
	var quote byte
	var dollarTag string
	inLine, inBlock := false, false
	depth := 0 // parens don't hide semicolons at depth 0 from engines, so count all
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		var next byte
		if i+1 < len(sql) {
			next = sql[i+1]
		}
		if inLine {
			if ch == '\n' {
				inLine = false
			}
			continue
		}
		if inBlock {
			if ch == '*' && next == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if dollarTag != "" {
			if ch == '$' && strings.HasPrefix(sql[i:], dollarTag) {
				i += len(dollarTag) - 1
				dollarTag = ""
			}
			continue
		}
		switch {
		case quote != 0:
			if ch == '\\' && quote == '\'' && i+1 < len(sql) {
				i++ // escaped char inside a string literal
				continue
			}
			if ch == quote {
				if i+1 < len(sql) && sql[i+1] == quote {
					i++ // doubled quote
					continue
				}
				quote = 0
			}
		case ch == '-' && next == '-':
			inLine = true
			i++
		case ch == '/' && next == '*':
			inBlock = true
			i++
		case ch == '$':
			tag := dollarTagAt(sql, i)
			if tag != "" {
				dollarTag = tag
				i += len(tag) - 1
			}
		case ch == '\'' || ch == '"' || ch == '`':
			quote = ch
		case ch == ';':
			// A trailing semicolon is punctuation, not a second statement:
			// "SELECT 1;" is what editors and models routinely produce. Only a
			// ';' with real content after it (ignoring further ';', whitespace
			// and comments) starts a second statement.
			if !isBlankRest(sql[i+1:]) {
				return false
			}
			return true
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		}
	}
	return true
}

// isBlankRest reports whether rest holds nothing but semicolons, whitespace and
// comments — the tail of a single statement.
func isBlankRest(rest string) bool {
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		switch {
		case ch == ';' || ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n':
			continue
		case ch == '-' && i+1 < len(rest) && rest[i+1] == '-':
			if nl := strings.IndexByte(rest[i:], '\n'); nl >= 0 {
				i += nl
				continue
			}
			return true
		case ch == '/' && i+1 < len(rest) && rest[i+1] == '*':
			end := strings.Index(rest[i+2:], "*/")
			if end < 0 {
				return true // unterminated block comment: nothing follows
			}
			i += 2 + end + 1
		default:
			return false
		}
	}
	return true
}

// dollarTagAt returns the dollar-quote tag starting at sql[i] ('$'), or "" —
// $$, $tag$, $fn$ and friends (PostgreSQL).
func dollarTagAt(sql string, i int) string {
	if i >= len(sql) || sql[i] != '$' {
		return ""
	}
	for j := i + 1; j < len(sql); j++ {
		c := sql[j]
		if c == '$' {
			return sql[i : j+1]
		}
		if !isWordByte(c) {
			return ""
		}
	}
	return ""
}

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

func (s *Server) query(connID, database, sql, format string) (string, error) {
	if connID == "" {
		return "", fmt.Errorf("缺少参数 connection")
	}
	if strings.TrimSpace(sql) == "" {
		return "", fmt.Errorf("缺少参数 sql")
	}
	// A multi-statement string cannot be classified by its first keyword: the
	// write after the ';' would ride along (see isSingleStatement).
	if !isSingleStatement(sql) {
		return "", fmt.Errorf("query 一次只接受一条语句（检测到多余的分号）；请拆开后逐条调用")
	}
	if !IsReadStatement(sql) {
		return "", fmt.Errorf("query 只接受只读语句（SELECT/WITH/SHOW/DESCRIBE/EXPLAIN）；写操作请用 execute")
	}
	result, err := s.catalog.Query(connID, database, sql, s.opts.MaxRows)
	if err != nil {
		return "", err
	}
	// 数据掩码: 行内容出机器前脱敏 (列名层 + 形态层)。MCP 是对外部 AI 的出口,
	// 出口的脱敏不可协商; masked 标记让客户端知道值是遮罩过的。
	return renderResult(MaskQueryResult(result), format)
}

func (s *Server) execute(connID, database, sql string) (string, error) {
	if connID == "" {
		return "", fmt.Errorf("缺少参数 connection")
	}
	if strings.TrimSpace(sql) == "" {
		return "", fmt.Errorf("缺少参数 sql")
	}
	// Same multi-statement refusal: each statement in a batch needs its own
	// classification, so the tool accepts one statement per call.
	if !isSingleStatement(sql) {
		return "", fmt.Errorf("execute 一次只接受一条语句（检测到多余的分号）；请拆开后逐条调用")
	}
	// A statement that reads is not a write; refusing it here would make the tool
	// surprising. It goes through the same read path (no confirmation needed).
	if IsReadStatement(sql) {
		return s.query(connID, database, sql, "")
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
