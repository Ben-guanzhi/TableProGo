package main

// MCP server wiring: the app's catalog behind internal/mcp, plus the two ways to
// reach it — a stdio child process (what Claude Desktop launches) and a loopback
// HTTP endpoint on the same token-guarded API.
//
// The protocol lives in internal/mcp; this file only adapts the app's existing
// calls to the Catalog interface, so the tool surface cannot drift from what the
// UI shows.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"tablepro-go/internal/mcp"
)

// mcpCatalog adapts App to mcp.Catalog. Every method reuses the call the UI uses,
// so a tool can never see a different catalog than the navigator.
type mcpCatalog struct{ app *App }

func (c mcpCatalog) Connections() []mcp.ConnectionInfo {
	c.app.connectionsMu.RLock()
	defer c.app.connectionsMu.RUnlock()
	out := make([]mcp.ConnectionInfo, 0, len(c.app.connections))
	for _, conn := range c.app.connections {
		// A plugin-backed connection holds no DB/KV handle yet is live; the
		// same three shapes isConnected() accepts count here too.
		if !conn.isConnected() {
			continue
		}
		out = append(out, mcp.ConnectionInfo{
			ID:     conn.ID,
			Name:   conn.Name,
			Type:   conn.Type,
			DBName: conn.Database,
		})
	}
	sortInfos(out)
	return out
}

// sortInfos keeps the list stable between calls: a map iteration order would make
// the model see the connections reshuffled on every call.
func sortInfos(list []mcp.ConnectionInfo) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].ID < list[j-1].ID; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func (c mcpCatalog) Schemas(connID string) ([]string, error) {
	return c.app.GetDatabases(connID)
}

func (c mcpCatalog) Tables(connID, database, schema string) ([]mcp.TableInfo, error) {
	conn, err := c.app.connection(connID)
	if err != nil {
		return nil, err
	}
	if database == "" {
		database = conn.Database
	}
	tables, err := c.app.GetTables(connID, database, schema)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.TableInfo, 0, len(tables))
	for _, t := range tables {
		out = append(out, mcp.TableInfo{Name: t.Name, Schema: t.Schema, Type: t.Type, Comment: t.Comment})
	}
	return out, nil
}

func (c mcpCatalog) Columns(connID, database, schema, table string) ([]mcp.ColumnInfo, error) {
	conn, err := c.app.connection(connID)
	if err != nil {
		return nil, err
	}
	if database == "" {
		database = conn.Database
	}
	columns, err := c.app.GetColumns(connID, database, schema, table)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.ColumnInfo, 0, len(columns))
	for _, col := range columns {
		out = append(out, mcp.ColumnInfo{
			Name:         col.Name,
			Type:         col.Type,
			Nullable:     col.Nullable,
			DefaultValue: col.DefaultValue,
			IsPrimaryKey: col.IsPrimaryKey,
			Comment:      col.Comment,
		})
	}
	return out, nil
}

// Query runs one statement. The tool has already classified it as a read (or the
// user approved a write); the row cap comes from the server, not the model.
func (c mcpCatalog) Query(connID, database, sql string, limit int) (*mcp.QueryResult, error) {
	// A tool call is not a tab, so it runs in autocommit: an AI client must never
	// find itself inside an editor's open transaction.
	result := c.app.ExecuteQuery(connID, sql, database)
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	out := &mcp.QueryResult{Truncated: result.Truncated}
	for _, col := range result.Columns {
		out.Columns = append(out.Columns, col.Name)
	}
	rows := result.Rows
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		out.Truncated = true
	}
	out.Rows = rows
	out.RowCount = len(rows)
	return out, nil
}

// mcpServer builds the server for this app. The write path is opt-in: it exists only
// when a confirmation channel is set, and the confirmation always goes to the UI.
func (a *App) mcpServer() *mcp.Server {
	opts := mcp.Options{
		MaxRows:       a.mcpMaxRows(),
		ServerName:    "tableprogo",
		ServerVersion: "0.1.0",
	}
	if a.mcpWritesEnabled() {
		opts.Level = mcp.ReadWrite
		opts.Confirm = a.confirmMCPWrite
	}
	return mcp.New(mcpCatalog{app: a}, opts)
}

// mcpMaxRows reads the cap from settings, defaulting to the package's.
func (a *App) mcpMaxRows() int {
	if n := a.GetSettings().MCPMaxRows; n > 0 {
		return n
	}
	return mcp.DefaultMaxRows
}

func (a *App) mcpWritesEnabled() bool {
	return a.GetSettings().MCPAllowWrites && a.mcpConfirmer != nil
}

// confirmMCPWrite asks the user through the UI. Without a UI attached (headless
// stdio) the answer is no: an unattended write path is exactly what this design
// refuses to have.
func (a *App) confirmMCPWrite(connID, sql string) bool {
	a.mcpConfirmerMu.Lock()
	confirmer := a.mcpConfirmer
	a.mcpConfirmerMu.Unlock()
	if confirmer == nil {
		return false
	}
	name := connID
	if conn, err := a.connection(connID); err == nil {
		name = conn.Name
	}
	return confirmer(name, sql)
}

// ConnectSavedConnections opens every saved connection so a headless MCP process has
// something to report. Errors are returned rather than logged: the stdio transport
// writes only protocol frames to stdout, and the caller decides where diagnostics go.
func (a *App) ConnectSavedConnections() []error {
	var errs []error
	for _, cfg := range a.savedConnections {
		if conn, ok := a.connections[cfg.ID]; ok && (conn.DB != nil || conn.KV != nil) {
			continue
		}
		if err := a.Connect(cfg.ID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", cfg.Name, err))
		}
	}
	return errs
}

// ---------- stdio transport ----------

// RunMCPStdio serves MCP over stdin/stdout until stdin closes. This is what an MCP
// client launches as a child process; it must write nothing else to stdout, so all
// diagnostics go to stderr.
func (a *App) RunMCPStdio(in io.Reader, out io.Writer) error {
	server := a.mcpServer()
	reader := bufio.NewReaderSize(in, 1<<20)
	writer := bufio.NewWriter(out)
	enc := json.NewEncoder(writer)

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
				if resp := server.Handle([]byte(trimmed)); resp != nil {
					// Encode adds the newline the framing needs.
					var payload json.RawMessage = resp
					if encErr := enc.Encode(payload); encErr != nil {
						fmt.Fprintf(os.Stderr, "mcp: write response: %v\n", encErr)
						return encErr
					}
					if flushErr := writer.Flush(); flushErr != nil {
						return flushErr
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// ---------- HTTP transport ----------

// serveMCP handles one JSON-RPC request over the loopback API. It exists so the
// running app can be inspected by an MCP client without restarting it as a child
// process.
func (a *App) serveMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	resp := a.mcpServer().Handle(body)
	w.Header().Set("Content-Type", "application/json")
	if resp == nil {
		// A notification: the protocol says the answer is an empty 202.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	_, _ = w.Write(resp)
}

// SetMCPConfirmer installs the UI's confirmation channel. Passing nil makes the
// server read-only again, which is the safe state.
func (a *App) SetMCPConfirmer(confirmer func(connName, sql string) bool) {
	a.mcpConfirmerMu.Lock()
	defer a.mcpConfirmerMu.Unlock()
	a.mcpConfirmer = confirmer
}
