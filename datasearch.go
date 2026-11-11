package main

// Whole-database DATA search (P2-10, the TablePlus "Search in Database"):
// find a text fragment inside the rows of every table/view of one database.
// This is distinct from the quick-open object search (search.go), which only
// matches NAMES — here the keyword is compared against each descriptive
// (text-ish) column of each object.
//
// The three guards the plan demands: a per-table row cap (a million-row table
// must not return a million hits), a per-database object cap (a 5000-table
// schema must not run for minutes), and cancellation — the walk honours the
// request context AND the connection's CancelQuery registry, and reports
// per-table progress so the UI can show what it is scanning right now.
//
// The per-engine SQL (bind vs inline literal, LIMIT vs TOP vs ROWNUM, LIKE vs
// ILIKE) lives in sqltext.SearchDataQuery; the keyword is escaped there as
// literal text, never as a pattern.

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/dbcore/dbcore"
	tableprosql "github.com/dbcore/dbcore/sqltext"
)

const (
	// A hit table shows at most this many rows; the grid already renders them,
	// and the next page of a real search is an SQL job, not this one.
	searchDataRowsPerTable = 50
	// Objects scanned per database before the search reports truncation.
	searchDataMaxTables = 200
	// Stop the walk once this many hit rows exist across tables: enough to
	// answer "where does this string appear", short enough to stay interactive.
	searchDataTotalRowCap = 500
)

// SearchDataEvent is one NDJSON event of a running data search.
type SearchDataEvent struct {
	Type   string `json:"type"` // start | table | done
	Tables int    `json:"tables,omitempty"`
	// Scanned/Total advance one per object, so a slow schema still shows life.
	Scanned   int      `json:"scanned,omitempty"`
	Total     int      `json:"total,omitempty"`
	Table     string   `json:"table,omitempty"`
	Schema    string   `json:"schema,omitempty"`
	Error     string   `json:"error,omitempty"`
	Columns   []Column `json:"columns,omitempty"`
	Rows      [][]any  `json:"rows,omitempty"`
	Hits      int      `json:"hits,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Cancelled bool     `json:"cancelled,omitempty"`
	Elapsed   float64  `json:"elapsedSeconds,omitempty"`
}

// SearchTableData streams the search's progress through emit. An emit error
// (the client went away) or ctx cancellation stops the walk between tables —
// individual statements also carry the context, so a query stuck on the
// server is cancelled too, matching the P0-4 rule for every other read.
func (a *App) SearchTableData(ctx context.Context, connID, database, keyword string, emit func(SearchDataEvent) error) error {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return emit(SearchDataEvent{Type: "done", Error: "请输入搜索内容"})
	}
	conn, err := a.connection(connID)
	if err != nil {
		return emit(SearchDataEvent{Type: "done", Error: err.Error()})
	}
	if conn.Type == "redis" {
		return emit(SearchDataEvent{Type: "done", Error: "Redis 没有行数据可搜索"})
	}

	runCtx, done := a.beginRunWith(ctx, connID)
	defer done()

	tables, err := a.GetTables(connID, database, "")
	if err != nil {
		return emit(SearchDataEvent{Type: "done", Error: err.Error()})
	}
	// Sorted so two runs agree on which objects the cap included.
	sort.Slice(tables, func(i, j int) bool {
		if tables[i].Schema != tables[j].Schema {
			return tables[i].Schema < tables[j].Schema
		}
		return tables[i].Name < tables[j].Name
	})
	truncated := false
	if len(tables) > searchDataMaxTables {
		tables, truncated = tables[:searchDataMaxTables], true
	}
	if err := emit(SearchDataEvent{Type: "start", Tables: len(tables)}); err != nil {
		return err
	}

	start := time.Now()
	totalHits := 0
	// Columns for the whole schema in one round trip when the driver supports
	// it (K-1): the search otherwise pays one GetColumns per table — up to
	// searchDataMaxTables round trips before the first table is searched. A
	// failed or absent batch degrades to the per-table read inside
	// searchOneTable, so a driver without the capability behaves exactly as
	// before. The search only asks whether a column is descriptive, so the
	// batch's catalog shape (Name + Type) is everything it consumes.
	batchCols := map[string][]ColumnInfo{}
	if drv, drvErr := a.sqlDriver(conn, database); drvErr == nil {
		if bc, ok := dbcore.AsBatchColumns(drv); ok {
			schema := resolveSchema(conn, database, "")
			if all, batchErr := bc.BatchColumns(runCtx, schema); batchErr == nil {
				for _, c := range all {
					batchCols[c.Table] = append(batchCols[c.Table], ColumnInfo{
						Name: c.Name,
						Type: c.Type,
					})
				}
			} else {
				log.Printf("datasearch: batch columns unavailable, falling back per table: %v", batchErr)
			}
		}
	}
	for i, tb := range tables {
		if runCtx.Err() != nil {
			return emit(SearchDataEvent{Type: "done", Cancelled: true, Elapsed: secondsSince(start)})
		}
		progress := SearchDataEvent{Type: "table", Scanned: i + 1, Total: len(tables), Schema: tb.Schema, Table: tb.Name}
		hits, err := searchOneTable(runCtx, a, conn, database, tb, keyword, batchCols[tb.Name])
		if err != nil {
			// One unreachable or broken object (a view over a dropped table is
			// the classic) must not kill the whole search — report and move on.
			progress.Error = err.Error()
			if emitErr := emit(progress); emitErr != nil {
				return emitErr
			}
			continue
		}
		progress.Hits = hits.RowCount
		progress.Columns = hits.Columns
		progress.Rows = hits.Rows
		totalHits += hits.RowCount
		if emitErr := emit(progress); emitErr != nil {
			return emitErr
		}
		if totalHits >= searchDataTotalRowCap {
			truncated = true
			break
		}
	}
	return emit(SearchDataEvent{Type: "done", Truncated: truncated, Elapsed: secondsSince(start)})
}

func secondsSince(start time.Time) float64 {
	return time.Since(start).Seconds()
}

// searchOneTable runs the keyword against the descriptive columns of one
// object. Objects with none (pure number/blob ledgers) answer zero hits
// without touching the server. Columns arrive pre-read when the driver served
// the whole schema in one batch (batch != nil); otherwise they are read here.
func searchOneTable(ctx context.Context, a *App, conn *Connection, database string, tb TableInfo, keyword string, batch []ColumnInfo) (QueryResult, error) {
	columns := batch
	if columns == nil {
		var err error
		columns, err = a.GetColumns(conn.ID, database, tb.Schema, tb.Name)
		if err != nil {
			return QueryResult{}, err
		}
	}
	var textCols []string
	for _, col := range columns {
		if tableprosql.NewColumnType(col.Type).IsDescriptive() {
			quoted, qerr := tableprosql.QuoteIdent(conn.Type, col.Name)
			if qerr != nil {
				return QueryResult{}, qerr
			}
			textCols = append(textCols, quoted)
		}
	}
	if len(textCols) == 0 {
		return QueryResult{}, nil
	}
	qualified, err := tableprosql.QualifiedTable(conn.Type, database, tb.Schema, tb.Name)
	if err != nil {
		return QueryResult{}, err
	}
	// One bind per column on the bound engines, one inline literal elsewhere —
	// the split SearchDataQuery owns.
	query, args, err := tableprosql.SearchDataQuery(conn.Type, qualified, textCols, keyword, searchDataRowsPerTable)
	if err != nil {
		return QueryResult{}, err
	}

	if pluginConnection(conn) {
		return QueryResult{}, fmt.Errorf("该操作暂不支持插件引擎连接")
	}

	db := conn.DB
	if isPostgresLike(conn.Type) {
		// Cross-database search needs a session bound to that database.
		if db, err = a.poolFor(conn, database); err != nil {
			return QueryResult{}, err
		}
	}
	result := runSelectCtx(ctx, db, query, args...)
	if result.Error != "" {
		return QueryResult{}, fmt.Errorf("%s", result.Error)
	}
	return result, nil
}
