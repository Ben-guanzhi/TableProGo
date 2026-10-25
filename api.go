package main

import (
	"tablepro-go/internal/llm"
	"tablepro-go/internal/sqlformat"

	"bytes"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"

	"github.com/Ben-guanzhi/TableProGo/tableproxlsx"
	"github.com/dbcore/dbcore"
)

// statusOK is the shared success payload of the mutating endpoints.
var statusOK = map[string]string{"status": "ok"}

// post registers one POST endpoint of the JSON API: decode the request body
// into Req, run call, and map the result onto the shared convention — 405 for
// a non-POST request, errStatus (or 400) for a body that does not decode or a
// call that fails, otherwise the encoded response. Every handler used to
// hand-write this same skeleton.
func post[Req any](a *App, mux *http.ServeMux, path string, errStatus int, call func(*App, Req) (any, error)) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var body Req
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		resp, err := call(a, body)
		if err != nil {
			writeError(w, errStatus, err.Error())
			return
		}
		writeJSON(w, resp)
	})
}

// serveExecuteStream answers /api/execute-stream with NDJSON, flushing each
// event so the grid paints before the server finishes reading. Hand-written
// because post() buffers a single JSON body.
func (a *App) serveExecuteStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var body queryRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	emit := func(ev StreamEvent) error {
		if err := enc.Encode(ev); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	// A client that disconnects cancels the statement: the request context is
	// the parent of the run context, and the emit error stops the loop.
	streamErr := a.StreamQuery(r.Context(), body.ConnID, body.Query, body.Database, emit)
	if streamErr != nil {
		_ = emit(StreamEvent{Type: "error", Error: streamErr.Error()})
	}
}

// Request shapes of the POST route table, kept in one place so the JSON
// contract stays reviewable at a glance.

type idRequest struct {
	ID string `json:"id"`
}

type connIDRequest struct {
	ConnID string `json:"connId"`
}

type connDatabaseRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
}

type tableRefRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
}

type sqlNameRequest struct {
	tableRefRequest
	Scope string `json:"scope"`
}

type createTableTemplateRequest struct {
	DBType string `json:"dbType"`
	Schema string `json:"schema"`
}

type queryRequest struct {
	ConnID   string `json:"connId"`
	Query    string `json:"query"`
	Database string `json:"database"`
}

// llmAskRequest is one question for the assistant.
type llmAskRequest struct {
	Question string `json:"question"`
}

// llmProposalRequest addresses one proposed statement.
type llmProposalRequest struct {
	ID string `json:"id"`
}

// llmConfigRequest carries the provider settings; Save says whether to persist them.
type llmConfigRequest struct {
	Config llm.Config `json:"config"`
	Save   bool       `json:"save"`
}

// txRequest addresses one tab's transaction.
type txRequest struct {
	ConnID   string `json:"connId"`
	TabID    string `json:"tabId"`
	Database string `json:"database"`
}

// tabQueryRequest is queryRequest plus the tab, so the statement joins that tab's
// open transaction.
type tabQueryRequest struct {
	ConnID   string `json:"connId"`
	TabID    string `json:"tabId"`
	Query    string `json:"query"`
	Database string `json:"database"`
}

type previewRequest struct {
	ConnID   string        `json:"connId"`
	Database string        `json:"database"`
	Schema   string        `json:"schema"`
	Table    string        `json:"table"`
	Limit    int           `json:"limit"`
	Offset   int           `json:"offset"`
	Filters  []TableFilter `json:"filters"`
	// After and Before are the key of a page already shown: the next page is
	// sought forward from After, the previous one backwards from Before. Both
	// are ignored when the object has no key to seek by.
	After  []string `json:"after"`
	Before []string `json:"before"`
}

type fkValuesRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Column   string `json:"column"`
	Limit    int    `json:"limit"`
}

type exportInsertRequest struct {
	ConnID   string          `json:"connId"`
	Database string          `json:"database"`
	Schema   string          `json:"schema"`
	Table    string          `json:"table"`
	Columns  []string        `json:"columns"`
	Rows     [][]interface{} `json:"rows"`
}

type exportXlsxRequest struct {
	Columns []tableproxlsx.Column `json:"columns"`
	Rows    [][]interface{}       `json:"rows"`
}

type tableActionRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Action   string `json:"action"`
	NewName  string `json:"newName"`
}

type kvValueRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Key      string `json:"key"`
}

type kvKeysRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Cursor   string `json:"cursor"`
	Count    int    `json:"count"`
}

type kvKeyTTLRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Key      string `json:"key"`
	Seconds  int64  `json:"seconds"`
}

type kvKeyRenameRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Key      string `json:"key"`
	NewName  string `json:"newName"`
}

type kvElementRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Key      string `json:"key"`
	Identity string `json:"identity"`
	ID       string `json:"id"`
	Column   string `json:"column"`
	Value    string `json:"value"`
}

type kvFieldRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type kvRowRequest struct {
	ConnID   string           `json:"connId"`
	Database string           `json:"database"`
	Key      string           `json:"key"`
	Kind     string           `json:"kind"`
	Member   string           `json:"member"`
	ID       string           `json:"id"`
	Fields   []kvFieldRequest `json:"fields"`
}

type historyRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Table    string `json:"table"`
	Limit    int    `json:"limit"`
}

type historyFilterRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Table    string `json:"table"`
}

type searchRequest struct {
	Pattern string `json:"pattern"`
	Limit   int    `json:"limit"`
}

// routes builds the JSON API's route table. Split out of the server start so tests
// can mount it on an httptest recorder instead of a real listener.
func (a *App) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// test-connection opens, pings and closes a throwaway connection without
	// touching saved state, and logs the attempt: hand-written on purpose.
	mux.HandleFunc("/api/test-connection", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var config ConnectionConfig
		if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		log.Printf("API test-connection: type=%s host=%s:%d db=%s user=%s encrypt=%s",
			config.Type, config.Host, config.Port, config.Database, config.Username, config.SSLMode)
		if err := a.TestConnection(config); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})

	// connections and settings are the only GET+POST endpoints; the rest of
	// the surface is POST-only and lives in the route table below.
	mux.HandleFunc("/api/connections", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			conns := a.GetConnections()
			writeJSON(w, conns)
		case http.MethodPost:
			var config ConnectionConfig
			if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
				writeError(w, 400, err.Error())
				return
			}
			if err := a.AddConnection(config); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			writeJSON(w, map[string]string{"status": "ok"})
		default:
			w.WriteHeader(405)
		}
	})

	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, a.GetSettings())
		case http.MethodPost:
			var settings AppSettings
			if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
				writeError(w, 400, err.Error())
				return
			}
			if err := a.SaveSettings(settings); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			writeJSON(w, map[string]string{"status": "ok"})
		default:
			w.WriteHeader(405)
		}
	})

	post(a, mux, "/api/connections/update", 500, func(a *App, config ConnectionConfig) (any, error) {
		if err := a.UpdateConnection(config); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/connections/delete", 500, func(a *App, body idRequest) (any, error) {
		if err := a.DeleteConnection(body.ID); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/connect", 400, func(a *App, body idRequest) (any, error) {
		if err := a.Connect(body.ID); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/disconnect", 500, func(a *App, body idRequest) (any, error) {
		if err := a.Disconnect(body.ID); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/databases", 400, func(a *App, body connIDRequest) (any, error) {
		return a.GetDatabases(body.ConnID)
	})
	post(a, mux, "/api/tables", 400, func(a *App, body tableRefRequest) (any, error) {
		return a.GetTables(body.ConnID, body.Database, body.Schema)
	})
	post(a, mux, "/api/schemas", 400, func(a *App, body connDatabaseRequest) (any, error) {
		return a.GetSchemas(body.ConnID, body.Database)
	})
	post(a, mux, "/api/columns", 400, func(a *App, body tableRefRequest) (any, error) {
		return a.GetColumns(body.ConnID, body.Database, body.Schema, body.Table)
	})
	post(a, mux, "/api/query-columns", 400, func(a *App, body queryRequest) (any, error) {
		return a.QueryColumns(body.ConnID, body.Query, body.Database)
	})
	post(a, mux, "/api/structure", 400, func(a *App, body tableRefRequest) (any, error) {
		return a.GetTableStructure(body.ConnID, body.Database, body.Schema, body.Table)
	})
	// The relationship diagram of one schema: every base table with its columns
	// and foreign keys, read-only, so the frontend can lay it out as an SVG.
	post(a, mux, "/api/er", 400, func(a *App, body tableRefRequest) (any, error) {
		return a.GetERDiagram(body.ConnID, body.Database, body.Schema)
	})
	// Schema comparison and synchronization. The comparison rules live in
	// dbcore/schemadiff; the apply route takes a plan id and entry ids rather
	// than SQL, so a client can only run statements this process computed.
	post(a, mux, "/api/compare", 400, func(a *App, body CompareRequest) (any, error) {
		return a.CompareSchemas(body.Source, body.Target, body.Options)
	})
	post(a, mux, "/api/compare/apply", 400, func(a *App, body ApplySyncRequest) (any, error) {
		return a.ApplySync(body)
	})
	post(a, mux, "/api/preview", 400, func(a *App, body previewRequest) (any, error) {
		return a.previewPage(body.ConnID, body.Database, body.Schema, body.Table,
			body.Limit, body.Offset, body.Filters, body.After, body.Before), nil
	})
	post(a, mux, "/api/fk-values", 400, func(a *App, body fkValuesRequest) (any, error) {
		return a.ForeignKeyValues(body.ConnID, body.Database, body.Schema, body.Table, body.Column, body.Limit)
	})
	post(a, mux, "/api/foreign-keys", 400, func(a *App, body tableRefRequest) (any, error) {
		return a.ForeignKeys(body.ConnID, body.Database, body.Schema, body.Table)
	})
	post(a, mux, "/api/export-insert", 400, func(a *App, body exportInsertRequest) (any, error) {
		script, err := a.InsertScript(body.ConnID, body.Database, body.Schema, body.Table, body.Columns, body.Rows)
		if err != nil {
			return nil, err
		}
		return map[string]string{"sql": script}, nil
	})
	post(a, mux, "/api/export-xlsx", 400, func(a *App, body exportXlsxRequest) (any, error) {
		workbook, err := a.WorkbookBase64(body.Columns, body.Rows)
		if err != nil {
			return nil, err
		}
		return map[string]string{"base64": workbook}, nil
	})
	mux.HandleFunc("/api/favorites", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, a.ListFavorites())
		case http.MethodPost:
			var body struct {
				ConnID   string `json:"connId"`
				Database string `json:"database"`
				Name     string `json:"name"`
				SQL      string `json:"sql"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeError(w, 400, err.Error())
				return
			}
			entry, err := a.AddFavorite(body.ConnID, body.Database, body.Name, body.SQL)
			if err != nil {
				writeError(w, 400, err.Error())
				return
			}
			writeJSON(w, entry)
		default:
			w.WriteHeader(405)
		}
	})
	post(a, mux, "/api/favorites/delete", 400, func(a *App, body struct {
		ID string `json:"id"`
	}) (any, error) {
		return nil, a.DeleteFavorite(body.ID)
	})

	post(a, mux, "/api/structure-preview", 400, func(a *App, body StructureEditRequest) (any, error) {
		return a.PreviewStructureEdit(body)
	})
	post(a, mux, "/api/structure-edit", 400, func(a *App, body StructureEditRequest) (any, error) {
		return a.ApplyStructureEdit(body)
	})
	post(a, mux, "/api/trigger-definition", 400, func(a *App, body tableRefRequest) (any, error) {
		def, err := a.GetTriggerDefinition(body.ConnID, body.Database, body.Schema, body.Table)
		if err != nil {
			return nil, err
		}
		return map[string]string{"definition": def}, nil
	})

	post(a, mux, "/api/backup", 400, func(a *App, body BackupRequest) (any, error) {
		return a.Backup(body)
	})

	mux.HandleFunc("/api/format-sql", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var body struct {
			SQL string `json:"sql"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		// The formatter lives in an internal package, like the script splitter:
		// one scanner, package-tested, no connection involved.
		writeJSON(w, map[string]string{"sql": sqlformat.Format(body.SQL)})
	})

	mux.HandleFunc("/api/security-notice", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		writeJSON(w, map[string]string{"notice": a.SecurityNotice()})
	})
	post(a, mux, "/api/table-action", 400, func(a *App, body tableActionRequest) (any, error) {
		statement, err := a.TableAction(body.ConnID, body.Database, body.Schema, body.Table, body.Action, body.NewName)
		if err != nil {
			// A Safe Mode pause is not a failure: hand the statement and the
			// reason back so the UI can confirm and resend.
			if reason, paused := isNeedsConfirmation(err); paused {
				return map[string]any{"needsConfirmation": true, "reason": reason, "statement": statement}, nil
			}
			return nil, err
		}
		return map[string]string{"status": "ok", "statement": statement}, nil
	})
	// Safe Mode: the UI confirms, then re-sends through this route, mirroring
	// /api/execute-confirmed — skipping the prompt cannot be a request flag.
	post(a, mux, "/api/table-action-confirmed", 400, func(a *App, body tableActionRequest) (any, error) {
		statement, err := a.TableActionConfirmed(body.ConnID, body.Database, body.Schema, body.Table, body.Action, body.NewName)
		if err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok", "statement": statement}, nil
	})
	post(a, mux, "/api/execute", 400, func(a *App, body queryRequest) (any, error) {
		return a.ExecuteQuery(body.ConnID, body.Query, body.Database), nil
	})
	// Safe Mode: the UI confirms, then re-sends through this route. Keeping it
	// separate means a client cannot bypass the prompt by setting a flag.
	post(a, mux, "/api/execute-confirmed", 400, func(a *App, body queryRequest) (any, error) {
		return a.ExecuteQueryConfirmed(body.ConnID, body.Query, body.Database), nil
	})
	// The plan reader: the statement is never executed (see explain.go), so it
	// has no Safe Mode gate and no confirmed variant.
	post(a, mux, "/api/explain", 400, func(a *App, body queryRequest) (any, error) {
		return a.ExplainQuery(body.ConnID, body.Query, body.Database), nil
	})
	// The AI assistant: a chat whose model reaches the database through the MCP
	// tools (llm.go). The provider settings live in their own file.
	// The assistant's write path: the model proposes, the user disposes. These
	// three routes are the whole of it — nothing else runs a proposed statement.
	post(a, mux, "/api/llm/proposals", 400, func(a *App, body connIDRequest) (any, error) {
		return map[string]any{"proposals": a.PendingProposals()}, nil
	})
	post(a, mux, "/api/llm/proposal/accept", 400, func(a *App, body llmProposalRequest) (any, error) {
		result, err := a.AcceptProposal(body.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": result}, nil
	})
	post(a, mux, "/api/llm/proposal/reject", 400, func(a *App, body llmProposalRequest) (any, error) {
		return map[string]bool{"rejected": a.RejectProposal(body.ID) == nil}, nil
	})
	post(a, mux, "/api/llm/ask", 400, func(a *App, body llmAskRequest) (any, error) {
		text, err := a.AskLLM(body.Question)
		return map[string]string{"answer": text}, err
	})
	post(a, mux, "/api/llm/history", 400, func(a *App, body connIDRequest) (any, error) {
		return map[string]any{"history": LLMHistory()}, nil
	})
	post(a, mux, "/api/llm/clear", 400, func(a *App, body connIDRequest) (any, error) {
		ClearLLMHistory()
		return map[string]bool{"cleared": true}, nil
	})
	post(a, mux, "/api/llm/config", 400, func(a *App, body llmConfigRequest) (any, error) {
		if body.Save {
			if err := SaveLLMConfig(body.Config); err != nil {
				return nil, err
			}
		}
		return map[string]any{"config": LoadLLMConfig(), "providers": LLMPresets()}, nil
	})

	// MCP over the loopback API: the same JSON-RPC handling the stdio child process
	// uses, so a running app can be inspected without a restart.
	mux.HandleFunc("/api/mcp", a.serveMCP)

	// Interactive transactions (txn.go): per-tab, so two editor tabs on one
	// connection keep separate transactions.
	post(a, mux, "/api/begin-tx", 400, func(a *App, body txRequest) (any, error) {
		status, err := a.BeginTransaction(body.ConnID, body.TabID, body.Database)
		return map[string]string{"status": status}, err
	})
	post(a, mux, "/api/commit-tx", 400, func(a *App, body txRequest) (any, error) {
		status, err := a.CommitTransaction(body.ConnID, body.TabID)
		return map[string]string{"status": status}, err
	})
	post(a, mux, "/api/rollback-tx", 400, func(a *App, body txRequest) (any, error) {
		status, err := a.RollbackTransaction(body.ConnID, body.TabID)
		return map[string]string{"status": status}, err
	})
	post(a, mux, "/api/tx-status", 400, func(a *App, body txRequest) (any, error) {
		return map[string]string{"status": a.TransactionStatus(body.TabID)}, nil
	})
	post(a, mux, "/api/execute-in-tab", 400, func(a *App, body tabQueryRequest) (any, error) {
		return a.ExecuteQueryInTab(body.ConnID, body.TabID, body.Query, body.Database), nil
	})
	post(a, mux, "/api/execute-in-tab-confirmed", 400, func(a *App, body tabQueryRequest) (any, error) {
		return a.ExecuteQueryConfirmedInTab(body.ConnID, body.TabID, body.Query, body.Database), nil
	})
	post(a, mux, "/api/cancel-query", 400, func(a *App, body connIDRequest) (any, error) {
		return map[string]int{"cancelled": a.CancelQuery(body.ConnID)}, nil
	})
	post(a, mux, "/api/import", 400, func(a *App, body ImportRequest) (any, error) {
		return a.ImportFile(body)
	})

	// Streaming results: NDJSON, one event per line, flushed per batch so the
	// grid paints before the server finishes reading. Hand-written because the
	// generic post() helper buffers a single JSON body.
	mux.HandleFunc("/api/execute-stream", a.serveExecuteStream)
	post(a, mux, "/api/kv-value", 400, func(a *App, body kvValueRequest) (any, error) {
		return a.GetKVValue(body.ConnID, body.Database, body.Key)
	})
	post(a, mux, "/api/kv-keys", 400, func(a *App, body kvKeysRequest) (any, error) {
		return a.GetKVKeyPage(body.ConnID, body.Database, body.Cursor, body.Count)
	})
	post(a, mux, "/api/kv-key-info", 400, func(a *App, body kvValueRequest) (any, error) {
		return a.GetKVKeyInfo(body.ConnID, body.Database, body.Key)
	})
	post(a, mux, "/api/kv-key-ttl", 400, func(a *App, body kvKeyTTLRequest) (any, error) {
		if err := a.SetKVKeyTTL(body.ConnID, body.Database, body.Key, body.Seconds); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/kv-key-rename", 400, func(a *App, body kvKeyRenameRequest) (any, error) {
		if err := a.RenameKVKey(body.ConnID, body.Database, body.Key, body.NewName); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/kv-key-delete", 400, func(a *App, body kvValueRequest) (any, error) {
		if err := a.DeleteKVKey(body.ConnID, body.Database, body.Key); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/kv-element", 400, func(a *App, body kvElementRequest) (any, error) {
		err := a.SetKVElement(body.ConnID, body.Database, dbcore.RedisElement{
			Key:      body.Key,
			Identity: body.Identity,
			ID:       body.ID,
			Column:   body.Column,
			Value:    body.Value,
		})
		if err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/kv-row-add", 400, func(a *App, body kvRowRequest) (any, error) {
		fields := make([]dbcore.RedisField, 0, len(body.Fields))
		for _, f := range body.Fields {
			fields = append(fields, dbcore.RedisField{Name: f.Name, Value: f.Value})
		}
		id, err := a.AddKVRow(body.ConnID, body.Database, dbcore.RedisRow{
			Key:    body.Key,
			Kind:   dbcore.RedisKeyType(body.Kind),
			Member: body.Member,
			ID:     body.ID,
			Fields: fields,
		})
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	})
	post(a, mux, "/api/history", 400, func(a *App, body historyRequest) (any, error) {
		return a.History(body.ConnID, body.Database, body.Table, body.Limit), nil
	})
	post(a, mux, "/api/clear-history", 400, func(a *App, body historyFilterRequest) (any, error) {
		return map[string]int{"removed": a.ClearHistory(body.ConnID, body.Database, body.Table)}, nil
	})
	post(a, mux, "/api/search-objects", 400, func(a *App, body searchRequest) (any, error) {
		return a.SearchObjects(body.Pattern, body.Limit)
	})
	post(a, mux, "/api/query-source", 400, func(a *App, body queryRequest) (any, error) {
		return a.QuerySource(body.ConnID, body.Query, body.Database), nil
	})
	post(a, mux, "/api/sql-name", 400, func(a *App, body sqlNameRequest) (any, error) {
		name, err := a.SqlName(body.ConnID, body.Database, body.Schema, body.Table, body.Scope)
		if err != nil {
			return nil, err
		}
		return map[string]string{"name": name}, nil
	})
	post(a, mux, "/api/create-table-template", 400, func(a *App, body createTableTemplateRequest) (any, error) {
		return map[string]string{"template": a.GetCreateTableTemplate(body.DBType, body.Schema)}, nil
	})
	post(a, mux, "/api/build-changes", 400, func(a *App, set ChangeSet) (any, error) {
		statements, err := a.PreviewChanges(set)
		if err != nil {
			return nil, err
		}
		return map[string][]string{"statements": statements}, nil
	})
	post(a, mux, "/api/apply-changes", 400, func(a *App, set ChangeSet) (any, error) {
		applied, err := a.ApplyChanges(set)
		if err != nil {
			return nil, err
		}
		return map[string]int64{"appliedRows": applied}, nil
	})

	return mux
}

func (a *App) startHTTPServerOnListener(listener net.Listener) {
	srv := &http.Server{Handler: corsMiddleware(authMiddleware(a.apiToken, a.routes()))}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()
}

// apiTokenHeader carries the per-process token from the webview to the API.
const apiTokenHeader = "X-API-Token"

// authMiddleware refuses any API call that does not present this process's
// token. The server binds 127.0.0.1:0, so the port is unpredictable — but any
// page the user happens to run on another localhost port could still POST to
// /api/execute, and CORS alone only keeps the *response* away from it. corsMiddleware
// stays outside, so the browser's OPTIONS preflight (which cannot carry the
// header) is still answered.
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(apiTokenHeader)), []byte(token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type injectHandler struct {
	inner  http.Handler
	inject string
}

func (h *injectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		h.inner.ServeHTTP(w, r)
		return
	}
	rec := &responseRecorder{ResponseWriter: w, statusCode: 200}
	h.inner.ServeHTTP(rec, r)
	if rec.statusCode != 200 {
		w.WriteHeader(rec.statusCode)
		rec.body.WriteTo(w)
		return
	}
	body := bytes.Replace(rec.body.Bytes(), []byte("<head>"), []byte("<head>"+h.inject), 1)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	return r.body.Write(b)
}

// localOriginRe matches the origins the frontend legitimately loads from:
// the packaged webview (same server, same origin — which needs no CORS header
// at all) and the vite dev server. Every other web origin is refused the CORS
// grant, so a page the user browses cannot read responses from this API — with
// the old `*` it could have run SQL against saved connections. Note that any
// localhost port still matches here, which is deliberate: authMiddleware's
// per-process token is what actually gates those origins.
var localOriginRe = regexp.MustCompile(`(?i)^https?://(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$`)

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); localOriginRe.MatchString(origin) {
			// Echo the specific origin, never `*`: a wildcard grant would
			// re-open the API to every web page.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			// X-API-Token is not CORS-safelisted, so the browser preflights it;
			// without it here the first authenticated call would fail.
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Token")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
