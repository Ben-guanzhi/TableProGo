package main

import (
	"tableprosql"

	"database/sql"
	"strings"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
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
}

type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
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
	return a.previewPage(connID, database, schema, table, limit, offset, filters, nil, nil)
}

// previewPage reads one page of an object. A cursor - the key of a page already
// on screen, sent back by whoever asked for the next one - replaces OFFSET for
// keyed objects, because counting rows off is what makes deep pages slow.
// Without a usable cursor the page is read exactly as it always was.
func (a *App) previewPage(connID string, database string, schema string, table string, limit int, offset int, filters []TableFilter, after []string, before []string) QueryResult {
	conn, err := a.connection(connID)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
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
func (a *App) ExecuteQuery(connID string, query string, database string) QueryResult {
	result := a.runQuery(connID, query, database)
	a.recordStatement(connID, database, query, result)
	return result
}

func (a *App) runQuery(connID string, query string, database string) QueryResult {
	conn, err := a.connection(connID)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	db, err := a.queryPool(conn, database)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	start := time.Now()
	if isSelectQuery(query) {
		limit := a.GetSettings().ResultLimit
		fetch := limit
		if limit > 0 {
			fetch = limit + 1
		}
		probe := withRowLimit(query, conn.Type, fetch)
		result := runSelect(db, probe, start)
		// The extra row is a probe only when the cap is ours: a statement with
		// a cap of its own runs untouched and is never marked truncated.
		if limit > 0 && probe != query && result.Error == "" && len(result.Rows) > limit {
			result.Rows = result.Rows[:limit]
			result.RowCount = limit
			result.Truncated = true
		}
		return result
	}
	result := executeCommand(db, query, start)
	if result.Error == "" {
		// A statement that ran can have moved a catalog (CREATE/DROP/RENAME,
		// and the menu's actions arrive through here too). Nothing here works
		// out which kinds touch which catalog: one number makes every quick-
		// open snapshot rebuild once, on the keystroke after the change,
		// instead of risking a stale list forever.
		a.catalogVersion.Add(1)
	}
	return result
}

func runSelect(db *sql.DB, query string, start time.Time, args ...interface{}) QueryResult {
	rows, err := db.Query(query, args...)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	defer rows.Close()

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

	var resultRows [][]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

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

	elapsed := time.Since(start).Seconds()
	return QueryResult{
		Columns:       colInfos,
		Rows:          resultRows,
		RowCount:      len(resultRows),
		ExecutionTime: elapsed,
	}
}

func executeCommand(db *sql.DB, query string, start time.Time) QueryResult {
	result, err := db.Exec(query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	affectedRows, _ := result.RowsAffected()
	lastInsertID, _ := result.LastInsertId()
	elapsed := time.Since(start).Seconds()
	return QueryResult{
		AffectedRows:  affectedRows,
		LastInsertID:  lastInsertID,
		ExecutionTime: elapsed,
	}
}

func isSelectQuery(query string) bool {
	query = strings.ToUpper(strings.TrimSpace(query))
	// WITH opens a CTE, which ends in a SELECT and returns rows: treating it
	// as a command ran it with the result thrown away. RowLimit already caps
	// a WITH ... SELECT the same way it caps a bare SELECT.
	return strings.HasPrefix(query, "SELECT") ||
		strings.HasPrefix(query, "WITH") ||
		strings.HasPrefix(query, "SHOW") ||
		strings.HasPrefix(query, "DESCRIBE") ||
		strings.HasPrefix(query, "EXPLAIN")
}

// withRowLimit caps a plain SELECT — or a WITH ... SELECT CTE — the way GUI
// clients do; the shapes live in the shared SQL text package both TablePro
// clients generate from.
func withRowLimit(query string, dbType string, limit int) string {
	return tableprosql.RowLimit(dbType, query, limit)
}
