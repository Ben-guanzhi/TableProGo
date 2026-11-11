package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/dberror"

	tableprosql "github.com/dbcore/dbcore/sqltext"

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

// dedupeColumnNames makes duplicate result-column names unique in place: the
// grid keys widths, filters and edits by name, so a JOIN projecting two `id`s
// would otherwise fold them into one column. The second occurrence becomes
// "id_2", and a suffix that itself collides keeps bumping. Case-insensitive,
// like the kernel's DisambiguateColumnNames, because engines fold unquoted
// identifiers. Order and every other field are preserved.
func dedupeColumnNames(cols []Column) {
	used := make(map[string]bool, len(cols))
	for i := range cols {
		base := cols[i].Name
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = base + "_" + strconv.Itoa(n)
		}
		used[strings.ToLower(name)] = true
		cols[i].Name = name
	}
}

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
	return a.previewPage(connID, "", database, schema, table, limit, offset, filters, nil, nil)
}

// previewPage reads one page of an object. A cursor - the key of a page already
// on screen, sent back by whoever asked for the next one - replaces OFFSET for
// keyed objects, because counting rows off is what makes deep pages slow.
// Preview page size bounds: an unset request gets defaultPreviewRows; a
// request is honored up to maxPreviewRows, the same cap table paging uses.
const (
	defaultPreviewRows = 100
	maxPreviewRows     = 5000
	// maxMaterializeRows is the hard bound on how many rows the non-streaming
	// execute path will materialize, regardless of ResultLimit. When the user
	// sets ResultLimit=0 (no limit), a SELECT * on a huge table would otherwise
	// OOM the client process. The streaming path (dbcore StreamQuerier) has no
	// such bound because it emits batches incrementally.
	maxMaterializeRows = 10000
)

// Without a usable cursor the page is read exactly as it always was.
func (a *App) previewPage(connID, tabID, database string, schema string, table string, limit int, offset int, filters []TableFilter, after []string, before []string) QueryResult {
	// Register the run scoped to the tab: without it the page read ran on
	// context.Background() and no cancel could reach it, unlike the query
	// editor's statements on the same connection.
	ctx, done := a.beginRunIn(context.Background(), connID, tabID)
	defer done()
	conn, err := a.connection(connID)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	// No explicit request falls back to a sane default page; a real request is
	// honored up to the paging cap, so asking for 5000 rows gets 5000 instead
	// of a silent downgrade to 100. This runs before the plugin branch too: a
	// plugin gets the same normalized page request, not a raw `PageSize: 0`.
	if limit <= 0 {
		limit = defaultPreviewRows
	}
	if limit > maxPreviewRows {
		limit = maxPreviewRows
	}
	if offset < 0 {
		offset = 0
	}
	// A plugin-backed connection renders the page itself: the client has no
	// dialect knowledge for an engine it does not compile, so the page cannot be
	// built from a WHERE and an ORDER BY here.
	if pluginConnection(conn) {
		return a.previewViaPlugin(ctx, conn, database, schema, table, limit, offset, filters)
	}

	db := conn.DB
	schema = defaultSchema(conn.Type, schema)
	if isPostgresLike(conn.Type) {
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

	result := runSelectCtx(ctx, db, query, cursorArgs...)
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
	return a.executeQuery(HistorySourceUser, "", connID, query, database, false)
}

// ExecuteQueryAs runs a statement attributed to source in the history audit:
// the AI surfaces (MCP tool calls, approved assistant proposals) must be
// distinguishable from statements the user typed. Safe Mode and transactions
// behave exactly as in ExecuteQuery.
func (a *App) ExecuteQueryAs(source string, connID string, query string, database string) QueryResult {
	return a.executeQuery(source, "", connID, query, database, false)
}

// ExecuteQueryInTab is ExecuteQuery for a statement that belongs to a tab, so it
// joins that tab's open transaction (txn.go) instead of running in autocommit.
// The empty tab id means "no transaction", which is what a background statement
// wants: it must not be captured by an editor's open transaction.
func (a *App) ExecuteQueryInTab(connID, tabID string, query string, database string) QueryResult {
	return a.executeQuery(HistorySourceUser, tabID, connID, query, database, false)
}

// ExecuteQueryConfirmedInTab is ExecuteQueryInTab after the Safe Mode prompt.
func (a *App) ExecuteQueryConfirmedInTab(connID, tabID string, query string, database string) QueryResult {
	return a.executeQuery(HistorySourceUser, tabID, connID, query, database, true)
}

// ExecuteQueryConfirmed is ExecuteQuery after the user acknowledged the Safe
// Mode warning. It is a distinct entry point so skipping the prompt has to be
// explicit in the caller's code, not a forgotten boolean.
func (a *App) ExecuteQueryConfirmed(connID string, query string, database string) QueryResult {
	return a.executeQuery(HistorySourceUser, "", connID, query, database, true)
}

func (a *App) executeQuery(source, tabID, connID string, query string, database string, force bool) QueryResult {
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
	ctx, done := a.beginRunIn(context.Background(), connID, tabID)
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
	a.recordStatementFrom(source, connID, database, query, result)
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
	if session := a.txSession(tabID); session.HasTx() {
		txID = session.TxID()
	}

	start := time.Now()
	result := a.runStatementForQuery(ctx, conn, database, drv, txID, query, start)

	// Auto-reconnect: when a statement fails because the connection died (server
	// crashed, network dropped, firewall killed the socket), reconnect and retry
	// once. Without this the user sees a raw transport error and must manually
	// reconnect — which is exactly what the DBA architecture analysis flagged.
	if result.Error != "" && txID == "" && dberror.Classify(fmt.Errorf("%s", result.Error)) == dberror.ConnectionBroken {
		log.Printf("runQuery: connection %q died, attempting reconnect", connID)
		if rerr := a.Connect(connID); rerr != nil {
			log.Printf("runQuery: reconnect failed: %v", rerr)
			return result
		}
		// Get the fresh connection and driver after reconnect.
		conn2, cerr := a.connection(connID)
		if cerr != nil {
			return result
		}
		drv2, derr := a.execDriver(conn2, database)
		if derr != nil {
			return QueryResult{Error: derr.Error()}
		}
		retryStart := time.Now()
		return a.runStatementForQuery(ctx, conn2, database, drv2, "", query, retryStart)
	}

	return result
}

// runStatementForQuery runs one statement (read or write) through the driver
// and applies the row-cap truncation for read queries. Split from runQuery so
// the reconnect path can retry cleanly.
func (a *App) runStatementForQuery(ctx context.Context, conn *Connection, database string, drv dbcore.SQLDriver, txID string, query string, start time.Time) QueryResult {
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
		// Invalidate this connection's quick-open snapshot only: the cache is
		// per connection (TP-21).
		a.bumpCatalog(conn.ID)
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
	var result QueryResult
	if txID == "" {
		result = runDriverStatement(ctx, drv, query, start)
	} else {
		m, err := a.txManagerFor(conn, database)
		if err != nil {
			return QueryResult{Error: err.Error()}
		}
		res, err := m.ExecuteInTx(ctx, txID, query)
		result = queryResultFrom(res, err)
		result.ExecutionTime = time.Since(start).Seconds()
	}
	// Hard bound: even when ResultLimit=0, the non-streaming execute path must
	// not materialize an unbounded result set. The dbcore driver drains the
	// whole cursor; this is the backstop that stops it at maxMaterializeRows.
	if result.Error == "" && len(result.Rows) > maxMaterializeRows {
		result.Rows = result.Rows[:maxMaterializeRows]
		result.RowCount = maxMaterializeRows
		result.Truncated = true
	}
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
		typeName := columnTypes[i].DatabaseTypeName()
		colInfos[i] = Column{
			Name:      col,
			Type:      typeName,
			Nullable:  nullable,
			IsDecimal: dbcore.IsExactDecimalType(typeName),
		}
	}
	// A JOIN projecting two `id`s must not yield two columns with one key.
	dedupeColumnNames(colInfos)

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
	truncated := false
	for rows.Next() {
		if len(resultRows) >= maxMaterializeRows {
			// Hard bound: even when ResultLimit=0, the non-streaming path must
			// not materialize an unbounded result set. Stop reading and mark
			// truncation so the caller knows the answer was cut short.
			truncated = true
			break
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return QueryResult{Error: err.Error()}
		}

		row := make([]interface{}, len(values))
		for i, v := range values {
			switch val := v.(type) {
			case []byte:
				row[i] = blobCell(val)
			case time.Time:
				row[i] = val.Format(time.RFC3339)
			case int64:
				row[i] = jsonSafeInt(val)
			case uint64:
				row[i] = jsonSafeUint(val)
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
		Truncated:     truncated,
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
// runEntry pairs a run's cancel with the tab that owns it: the cancel button
// must stop ITS statement, not every statement sharing the connection — one
// tab's cancel used to kill a neighbouring tab's long query too.
type runEntry struct {
	cancel context.CancelFunc
	tabID  string
}

func (a *App) beginRun(connID string) (context.Context, func()) {
	return a.beginRunIn(context.Background(), connID, "")
}

// beginRunIn is beginRun with the owning tab recorded; tabID is empty for
// connection-wide work (backup, compare, catalog refresh).
func (a *App) beginRunIn(parent context.Context, connID, tabID string) (context.Context, func()) {
	return a.beginRunWithTab(parent, connID, tabID)
}

// beginRunWith is beginRunWithTab with no parent deadline semantics change —
// kept for the streaming HTTP path.
func (a *App) beginRunWith(parent context.Context, connID string) (context.Context, func()) {
	return a.beginRunWithTab(parent, connID, "")
}

func (a *App) beginRunWithTab(parent context.Context, connID, tabID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	id := a.runSeq.Add(1)
	a.runsMu.Lock()
	if a.runs == nil {
		a.runs = make(map[string]map[int64]*runEntry)
	}
	if a.runs[connID] == nil {
		a.runs[connID] = make(map[int64]*runEntry)
	}
	a.runs[connID][id] = &runEntry{cancel: cancel, tabID: tabID}
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

// CancelQuery stops the statements still running on connID and reports how
// many were cancelled. An empty tabID means every run on the connection; a
// tabID narrows it to that tab's runs — the editor's cancel button names its
// tab so a long query in a neighbouring tab survives. Cancelling a context is
// what makes the driver abort the statement server-side (pgx/go-sql-driver
// both honor it); the connection pool discards the broken connection on its own.
func (a *App) CancelQuery(connID, tabID string) int {
	a.runsMu.Lock()
	defer a.runsMu.Unlock()
	set := a.runs[connID]
	n := 0
	for _, e := range set {
		if tabID != "" && e.tabID != tabID {
			continue
		}
		e.cancel()
		n++
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
		// The kernel's bounded reader stops at DriverMaxRows and flags it:
		// propagating the flag here is what lets the grid say "the result
		// was cut" instead of showing a full-looking page.
		Truncated: res.Truncated,
	}
	for i, c := range res.Columns {
		out.Columns[i] = Column{
			Name:      c.Name,
			Table:     c.Table,
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
// jsonSafeInt maps an integer cell onto a JSON-safe value. Beyond
// Number.MAX_SAFE_INTEGER (|2^53|) a JSON number rounds through the browser's
// res.json() — the grid would display AND edit-save a different value, and key
// predicates built from the rounded cell hit the wrong row. Snowflake-style IDs
// live exactly there. Those cross as exact digit text (the save path's
// exactNumberLiteral round-trips it无损); the grid's numeric ordering already
// handles numeric strings. Shared by the buffered cellValue, the streaming
// streamCell, and the preview runSelectCtx so all three exits agree.
func jsonSafeInt(v int64) interface{} {
	if v > 1<<53 || v < -(1<<53) {
		return strconv.FormatInt(v, 10)
	}
	return v
}

// jsonSafeUint is jsonSafeInt for unsigned integers: a u64 past 2^53 also
// rounds in the browser's res.json().
func jsonSafeUint(v uint64) interface{} {
	if v > 1<<53 {
		return strconv.FormatUint(v, 10)
	}
	return v
}

// blobCell renders a binary cell for the wire. JSON strings cannot carry
// invalid UTF-8: encoding/json replaces every bad byte with U+FFFD, so raw
// binary arrived as mojibake and a copy/edit round-tripped the corruption.
// Valid-UTF-8 bytes (a TEXT column the driver handed over as []byte) stay
// text; anything else renders as a hex preview prefixed `\x` — display and
// copy stay faithful, and the prefix keeps the value from being mistaken for
// (and saved back as) text. Shared with the streaming and preview paths.
func blobCell(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	const hexPreview = 64
	const prefix = `\x`
	n := len(b)
	if n > hexPreview {
		n = hexPreview
	}
	out := prefix + hex.EncodeToString(b[:n])
	if len(b) > hexPreview {
		out += fmt.Sprintf("…(%d bytes)", len(b))
	}
	return out
}

func cellValue(v dbcore.Value) interface{} {
	switch v.Kind {
	case dbcore.ValInt:
		return jsonSafeInt(v.I)
	case dbcore.ValFloat:
		return v.F
	case dbcore.ValText:
		return v.S
	case dbcore.ValBlob:
		return blobCell(v.B)
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
