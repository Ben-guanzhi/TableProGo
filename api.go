package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"

	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"

	"tableproxlsx"
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

type queryRequest struct {
	ConnID   string `json:"connId"`
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

func (a *App) startHTTPServerOnListener(listener net.Listener) {
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
		db, err := createConnection(config)
		if err != nil {
			log.Printf("API createConnection error: %v", err)
			writeError(w, 400, err.Error())
			return
		}
		defer db.Close()
		if err := db.Ping(); err != nil {
			log.Printf("API db.Ping error: %v", err)
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
	post(a, mux, "/api/table-action", 400, func(a *App, body tableActionRequest) (any, error) {
		statement, err := a.TableAction(body.ConnID, body.Database, body.Schema, body.Table, body.Action, body.NewName)
		if err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok", "statement": statement}, nil
	})
	post(a, mux, "/api/execute", 400, func(a *App, body queryRequest) (any, error) {
		return a.ExecuteQuery(body.ConnID, body.Query, body.Database), nil
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

	go func() {
		srv := &http.Server{Handler: corsMiddleware(mux)}
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()
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
// grant, so a page the user browses cannot read responses from this API —
// with the old `*` it could have run SQL against saved connections.
var localOriginRe = regexp.MustCompile(`(?i)^https?://(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$`)

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); localOriginRe.MatchString(origin) {
			// Echo the specific origin, never `*`: a wildcard grant would
			// re-open the API to every web page.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
