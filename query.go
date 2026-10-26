package main

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/dbcore/dbcore"

	tableprosql "github.com/dbcore/dbcore/sqltext"
	"tablepro-go/internal/txn"

	"tablepro-go/internal/catalogmodel"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"
)

type QueryResult struct {
	Columns       []Column        `json:"columns"`
	Rows          [][]interface{} `json:"rows"`
	RowCount      int             `json:"rowCount"`
	AffectedRows  int64           `json:"affectedRows"`
	LastInsertID  int64           `json:"lastInsertId"`
	ExecutionTime float64         `json:"executionTime"`
	Error         string          `json:"error,omitempty"`
	// Truncated says the row cap cut the answer short, so a grid full of
	// exactly "limit" rows is not mistaken for the whole story.
	Truncated bool `json:"truncated,omitempty"`
	// StartCursor and EndCursor are the key of a page's first and last row, as
	// text. A client that wants the page beside this one hands one of them back
	// and the server seeks by it instead of counting rows past. They stay empty
	// for an object with no usable key, whose pages are read by OFFSET.
	StartCursor []string `json:"startCursor,omitempty"`
	EndCursor   []string `json:"endCursor,omitempty"`
	// NeedsConfirmation says Safe Mode paused the statement: nothing ran, and
	// Error carries the reason. The UI confirms, then re-sends through
	// ExecuteQueryConfirmed.
	NeedsConfirmation bool `json:"needsConfirmation,omitempty"`
}

// Column is the result-grid descriptor; the shape lives in catalogmodel.
type Column = catalogmodel.Column

// QueryColumns resolves the table a query reads from and returns its columns,
// which is how the result grid finds column comments for "SELECT *" output.
// Names without a database prefix are looked up in database, then in the
// connection's own. Sub-selects, joins and unresolvable names return nothing.
func (a *App) QueryColumns(connID string, query string, database string) ([]ColumnInfo, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}

	match := queryTableRe.FindStringSubmatch(sqlCommentRe.ReplaceAllString(query, " "))
	if match == nil {
		return nil, nil
	}
	ref := parseTableRef(match[1])
	if ref.table == "" {
		return nil, nil
	}

	switch {
	case ref.database != "":
		database = ref.database
	case database == "":
		database = conn.Database
	}
	return a.GetColumns(connID, database, ref.schema, ref.table)
}

// Preview reads the first rows of a table so the sidebar can show data without
// the user writing a query, narrowed by the filter bar when it holds any.
func (a *App) Preview(connID string, database string, schema string, table string, limit int, offset int, filters []TableFilter) QueryResult {
	return a.previewPage(connID, database, schema, table, limit, offset, filters, nil, nil)
}

// previewPage reads one page of an object. A cursor - the key of a page already
// on screen, sent back by whoever asked for the next one - replaces OFFSET for
// keyed objects, because counting rows off is what makes deep pages slow.
// Preview page size bounds: an unset request gets defaultPreviewRows; a
// request is honored up to maxPreviewRows, the same cap table paging uses.
const (
	defaultPreviewRows = 100
	maxPreviewRows     = 5000
)

// Without a usable cursor the page is read exactly as it always was.
func (a *App) previewPage(connID string, database string, schema string, table string, limit int, offset int, filters []TableFilter, after []string, before []string) QueryResult {
	conn, err := a.connection(connID)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	// A plugin-backed connection renders the page itself: the client has no
	// dialect knowledge for an engine it does not compile, so the page cannot be
	// built from a WHERE and an ORDER BY here.
	if pluginConnection(conn) {
		return a.previewViaPlugin(conn, database, schema, table, limit, offset, filters)
	}
	// No explicit request falls back to a sane default page; a real request is
	// honored up to the paging cap, so asking for 5000 rows gets 5000 instead
	// of a silent downgrade to 100.
	if limit <= 0 {
		limit = defaultPreviewRows
	}
	if limit > maxPreviewRows {
		limit = maxPreviewRows
	}
	if offset < 0 {
		offset = 0
	}

	db := conn.DB
	schema = defaultSchema(conn.Type, schema)
	if conn.Type == "postgresql" {
		if db, err = a.poolFor(conn, database); err != nil {
			return QueryResult{Error: err.Error()}
		}
	}
	// The same renderer the update builder uses, so the grid and any edit
	// started from it address one and the same object.
	// One catalog read serves the page: the key ORDER BY and the filter WHERE
	// both need the columns, and a page flip used to pay two round trips.
	columns, columnsErr := a.GetColumns(connID, database, schema, table)
	if columnsErr != nil && len(filters) > 0 {
		return QueryResult{Error: columnsErr.Error()}
	}
	orderBy := pageOrderFrom(conn.Type, columns, columnsErr)
	where, err := tableWhereFrom(conn, table, columns, filters)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	// A cursor of the wrong shape is not something the user can act on, and the
	// page number they are on is still known, so the step is taken by OFFSET.
	keys := keysetKeys(conn.Type, columns, columnsErr)
	cursor, backward := seekCursor(keys, after, before)
	var cursorArgs []interface{}
	if cursor != nil {
		seek, args, seekErr := keysetWhere(conn.Type, keys, cursor, backward)
		if seekErr != nil {
			cursor, backward, cursorArgs = nil, false, nil
		} else {
			cursorArgs = args
			if where == "" {
				where = " WHERE " + seek
			} else {
				where += " AND " + seek
			}
			orderBy = keysetOrder(keys, backward)
			offset = 0
		}
	}

	// Both need an ORDER BY to page. A constant one is what a keyless object
	// falls back to: the rows it splits across pages are then arbitrary, but
	// they are not shown twice or dropped.
	if conn.Type == "sqlserver" || conn.Type == "oracle" {
		if orderBy == "" {
			if conn.Type == "sqlserver" {
				orderBy = " ORDER BY (SELECT NULL)"
			} else {
				orderBy = " ORDER BY 1"
			}
		}
	}

	// where already carries its leading WHERE keyword from tableWhereFrom.
	query, _, err := tableprosql.PreviewPageQuery(conn.Type, database, schema, table, where, orderBy, limit, offset)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	result := runSelect(db, query, time.Now(), cursorArgs...)
	if result.Error != "" {
		return result
	}
	if backward {
		reversePage(result.Rows)
	}
	// Every keyed page hands back its own edges, including the ones read by
	// OFFSET: the first page then has a cursor to step from.
	if len(keys) > 0 {
		result.StartCursor, result.EndCursor = pageCursors(result.Rows, keys, result.Columns)
	}
	return result
}

// seekCursor picks the direction a page is stepped in. Only one cursor can
// apply at a time, and a cursor whose length does not match the key is the
// catalog having changed under the grid, which is what OFFSET is for.
func seekCursor(keys []keyColumn, after []string, before []string) ([]string, bool) {
	if len(keys) == 0 {
		return nil, false
	}
	if len(after) == len(keys) {
		return after, false
	}
	if len(before) == len(keys) {
		return before, true
	}
	return nil, false
}

// pageOrderFrom sorts a browsed table by its key so consecutive pages cannot
// show one row twice or skip it. An object with no key - or one whose columns
// could not be read - gets no ordering at all rather than a guessed one.
func pageOrderFrom(dbType string, columns []ColumnInfo, columnsErr error) string {
	if columnsErr != nil {
		return ""
	}
	var keys []string
	for _, col := range columns {
		if !col.IsPrimaryKey {
			continue
		}
		name, err := quoteIdent(dbType, col.Name)
		if err != nil {
			return ""
		}
		keys = append(keys, name)
	}
	if len(keys) == 0 {
		return ""
	}
	return " ORDER BY " + strings.Join(keys, ", ")
}

// ExecuteQuery runs a statement against database, which defaults to the
// database the connection opened with. An empty or matching name reuses the
// main pool, so only cross-database tabs pay for an extra connection.
//
// Safe Mode (see safemode.go) may stop the statement before it reaches the
// server: a blocked statement returns Error, a statement needing confirmation
// returns NeedsConfirmation + Error with nothing executed. The UI confirms and
// re-sends through ExecuteQueryConfirmed.
func (a *App) ExecuteQuery(connID string, query string, database string) QueryResult {
	return a.executeQuery("", connID, query, database, false)
}

// ExecuteQueryInTab is ExecuteQuery for a statement that belongs to a tab, so it
// joins that tab's open transaction (txn.go) instead of running in autocommit.
// The empty tab id means "no transaction", which is what a background statement
// wants: it must not be captured by an editor's open transaction.
func (a *App) ExecuteQueryInTab(connID, tabID string, query string, database string) QueryResult {
	return a.executeQuery(tabID, connID, query, database, false)
}

// ExecuteQueryConfirmedInTab is ExecuteQueryInTab after the Safe Mode prompt.
func (a *App) ExecuteQueryConfirmedInTab(connID, tabID string, query string, database string) QueryResult {
	return a.executeQuery(tabID, connID, query, database, true)
}

// ExecuteQueryConfirmed is ExecuteQuery after the user acknowledged the Safe
// Mode warning. It is a distinct entry point so skipping the prompt has to be
// explicit in the caller's code, not a forgotten boolean.
func (a *App) ExecuteQueryConfirmed(connID string, query string, database string) QueryResult {
	return a.executeQuery("", connID, query, database, true)
}

func (a *App) executeQuery(tabID, connID string, query string, database string, force bool) QueryResult {
	_, kvErr := a.kvConnection(connID)
	if kvErr != nil {
		// Safe Mode guards SQL statements; raw Redis commands have their own
		// semantics (DEL/FLUSHDB do not take WHERE) and are left alone.
		if guard := guardWrite(a.GetSettings().SafeMode, query); guard.Blocked || (guard.NeedsConfirmation && !force) {
			return QueryResult{Error: guard.Reason, NeedsConfirmation: guard.NeedsConfirmation && !force}
		}
	}
	// A tab with an open transaction runs inside it, and is told when the
	// transaction is bound to another database rather than silently escaping it.
	session := a.txSession(tabID)
	if err := session.Guard(database); err != nil {
		return QueryResult{Error: err.Error()}
	}
	ctx, done := a.beginRun(connID)
	defer done()
	var result QueryResult
	if kvErr == nil {
		conn, _ := a.kvConnection(connID)
		result = a.runKVCommand(conn, database, query)
	} else {
		result = a.runQuery(ctx, tabID, connID, query, database)
	}
	// A failed statement aborts the transaction on most engines; the session has
	// to remember that so the UI stops offering more.
	session.Record(errorOf(result))
	a.recordStatement(connID, database, query, result)
	return result
}

func (a *App) runQuery(ctx context.Context, tabID, connID, query, database string) QueryResult {
	conn, err := a.connection(connID)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	drv, err := a.execDriver(conn, database)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	// A tab with an open transaction runs the statement inside it. Without this the
	// transaction would only be bookkeeping: the statement would go through the
	// pool in autocommit and commit/rollback would govern nothing.
	txID := ""
	if session := a.txSession(tabID); session.Status == txn.Active || session.Status == txn.Failed {
		txID = session.ID
	}

	start := time.Now()
	if dbcore.IsReadQuery(query) {
		limit := a.GetSettings().ResultLimit
		fetch := limit
		if limit > 0 {
			fetch = limit + 1
		}
		probe := withRowLimit(query, conn.Type, fetch)
		result := a.runStatement(ctx, conn, database, drv, txID, probe, start)
		// The extra row is a probe only when the cap is ours: a statement with
		// a cap of its own runs untouched and is never marked truncated.
		if limit > 0 && probe != query && result.Error == "" && len(result.Rows) > limit {
			result.Rows = result.Rows[:limit]
			result.RowCount = limit
			result.Truncated = true
		}
		return result
	}
	result := a.runStatement(ctx, conn, database, drv, txID, query, start)
	if result.Error == "" && !movesDataOnly(query) {
		// A statement that ran can have moved a catalog (CREATE/DROP/RENAME,
		// and the menu's actions arrive through here too). Plain row DML
		// (INSERT/UPDATE/DELETE/MERGE/REPLACE) cannot, so it must not
		// invalidate: the next quick-open keystroke would otherwise rebuild
		// every connection's snapshot after each edit. Anything else — known
		// DDL or an exotic statement — still bumps, keeping the "stale list"
		// risk on the conservative side.
		a.catalogVersion.Add(1)
	}
	return result
}

// runStatement runs one statement, inside the tab's transaction when txID is set.
// The transaction's own registry serializes access, so a parallel statement on the
// same tab is refused rather than interleaved.
func (a *App) runStatement(
	ctx context.Context,
	conn *Connection,
	database string,
	drv dbcore.SQLDriver,
	txID string,
	query string,
	start time.Time,
) QueryResult {
	if txID == "" {
		return runDriverStatement(ctx, drv, query, start)
	}
	m, err := a.txManagerFor(conn, database)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	res, err := m.ExecuteInTx(ctx, txID, query)
	result := queryResultFrom(res, err)
	result.ExecutionTime = time.Since(start).Seconds()
	return result
}

// execDriver returns the driver a user statement runs through. The pool comes
// from queryPool, not poolFor: a tab on another database needs a session of its
// own for MySQL and SQL Server, while metadata reads qualify the database name
// on the main pool.
func (a *App) execDriver(conn *Connection, database string) (dbcore.SQLDriver, error) {
	// A plugin-backed connection has no *sql.DB to pick a pool from: the child
	// process holds the one handle.
	if pluginConnection(conn) {
		return a.pluginDriver(conn)
	}
	db, err := a.queryPool(conn, database)
	if err != nil {
		return nil, err
	}
	return driverFor(conn, db)
}

// movesDataOnly reports whether a non-select statement only moves row data and
// so cannot have changed which objects exist. The list stays small and matches
// the DML keywords; everything else (DDL, vacuum, unrecognized) is treated as
// potentially catalog-changing.
func movesDataOnly(query string) bool {
	q := strings.ToUpper(strings.TrimSpace(query))
	for _, kw := range []string{"INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE"} {
		if strings.HasPrefix(q, kw) {
			return true
		}
	}
	return false
}

// runSelect keeps the original shape for callers without a context.
func runSelect(db *sql.DB, query string, start time.Time, args ...interface{}) QueryResult {
	return runSelectCtx(context.Background(), db, query, args...)
}

// runSelectCtx is runSelect with a caller-owned context, so a version probe
// (backup) aborts with the run instead of hanging on a dead server.
func runSelectCtx(ctx context.Context, db *sql.DB, query string, args ...interface{}) QueryResult {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	defer rows.Close()

	start := time.Now()
	columns, err := rows.Columns()
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	colInfos := make([]Column, len(columns))
	for i, col := range columns {
		nullable, _ := columnTypes[i].Nullable()
		colInfos[i] = Column{
			Name:     col,
			Type:     columnTypes[i].DatabaseTypeName(),
			Nullable: nullable,
		}
	}

	// The scan destinations are reused across rows (as fynequery's runSelectCtx
	// already does): one allocation for values and one for valuePtrs instead of
	// two per row. Only the interpreted row - the slice the caller keeps - is
	// allocated per iteration.
	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range values {
		valuePtrs[i] = &values[i]
	}
	var resultRows [][]interface{}
	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return QueryResult{Error: err.Error()}
		}

		row := make([]interface{}, len(values))
		for i, v := range values {
			switch val := v.(type) {
			case []byte:
				row[i] = string(val)
			case time.Time:
				row[i] = val.Format(time.RFC3339)
			default:
				row[i] = val
			}
		}
		resultRows = append(resultRows, row)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{Error: err.Error()}
	}

	elapsed := time.Since(start).Seconds()
	return QueryResult{
		Columns:       colInfos,
		Rows:          resultRows,
		RowCount:      len(resultRows),
		ExecutionTime: elapsed,
	}
}

// runDriverStatement runs one statement through the dbcore driver and maps the
// kernel's result onto the shape the grid consumes.
func runDriverStatement(ctx context.Context, drv dbcore.SQLDriver, query string, start time.Time) QueryResult {
	res, err := drv.Execute(ctx, query)
	result := queryResultFrom(res, err)
	result.ExecutionTime = time.Since(start).Seconds()
	return result
}

// beginRun registers a cancellable statement for connID. The returned done
// removes it; CancelQuery finds it by connection.
func (a *App) beginRun(connID string) (context.Context, func()) {
	return a.beginRunWith(context.Background(), connID)
}

// beginRunWith is beginRun with a parent context, so a streaming HTTP request
// also stops when the client disconnects.
func (a *App) beginRunWith(parent context.Context, connID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	id := a.runSeq.Add(1)
	a.runsMu.Lock()
	if a.runs == nil {
		a.runs = make(map[string]map[int64]context.CancelFunc)
	}
	if a.runs[connID] == nil {
		a.runs[connID] = make(map[int64]context.CancelFunc)
	}
	a.runs[connID][id] = cancel
	a.runsMu.Unlock()
	return ctx, func() {
		a.runsMu.Lock()
		if set := a.runs[connID]; set != nil {
			delete(set, id)
			if len(set) == 0 {
				delete(a.runs, connID)
			}
		}
		a.runsMu.Unlock()
		cancel()
	}
}

// CancelQuery stops every statement still running on connID and reports how
// many were cancelled. Cancelling a context is what makes the driver abort the
// statement server-side (pgx/go-sql-driver both honor it); the connection pool
// discards the broken connection on its own.
func (a *App) CancelQuery(connID string) int {
	a.runsMu.Lock()
	defer a.runsMu.Unlock()
	set := a.runs[connID]
	n := len(set)
	for _, cancel := range set {
		cancel()
	}
	// Do not delete the map here: each cancelled run's done() removes its entry,
	// and deleting the map first would race with those calls.
	return n
}

func queryResultFrom(res *dbcore.QueryResult, err error) QueryResult {
	if err != nil {
		return QueryResult{Error: friendlyDBError(err)}
	}
	out := QueryResult{
		Columns:      make([]Column, len(res.Columns)),
		AffectedRows: res.AffectedRows,
		LastInsertID: res.LastInsertID,
	}
	for i, c := range res.Columns {
		out.Columns[i] = Column{
			Name:      c.Name,
			Type:      c.Type,
			Nullable:  c.Nullable,
			IsDecimal: dbcore.IsExactDecimalType(c.Type),
		}
	}
	out.Rows = make([][]interface{}, len(res.Rows))
	for i, row := range res.Rows {
		cells := make([]interface{}, len(row))
		for j, v := range row {
			cells[j] = cellValue(v)
		}
		out.Rows[i] = cells
	}
	out.RowCount = len(out.Rows)
	return out
}

// cellValue maps a kernel cell onto the JSON-friendly value the grid renders:
// blobs stay text (the local scan showed their bytes) and times keep the
// RFC3339 form the frontend already parses. Decimals cross as their exact
// digit TEXT — a JSON number would round them through float64 on the JS side,
// which is the precision loss ValDecimal exists to prevent; the frontend
// re-attaches numeric ORDERING from the column type (gridView cellKeyFor).
func cellValue(v dbcore.Value) interface{} {
	switch v.Kind {
	case dbcore.ValInt:
		return v.I
	case dbcore.ValFloat:
		return v.F
	case dbcore.ValText:
		return v.S
	case dbcore.ValBlob:
		return string(v.B)
	case dbcore.ValBool:
		return v.Bool
	case dbcore.ValTime:
		return v.T.Format(time.RFC3339)
	case dbcore.ValDecimal:
		return v.S
	default:
		return nil
	}
}

// isSelectQuery reports whether a statement returns rows. The kernel decides
// (SELECT, WITH, SHOW, EXPLAIN, DESC, PRAGMA, VALUES, TABLE, RETURNING), so the
// row-limit path and the grid's source-parse path can never disagree.
func isSelectQuery(query string) bool {
	return dbcore.IsReadQuery(query)
}

// withRowLimit caps a plain SELECT — or a WITH ... SELECT CTE — the way GUI
// clients do; the shapes live in the shared SQL text package both TablePro
// clients generate from.
func withRowLimit(query string, dbType string, limit int) string {
	return tableprosql.RowLimit(dbType, query, limit)
}
