package main

// The edit-to-SQL builder lives in internal/changes; this file keeps the App
// wiring (connection lookup, catalog read, transaction) and the type aliases the
// rest of the package (and the JSON API) already uses.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/dbcore/dbcore"
	tableprosql "github.com/dbcore/dbcore/sqltext"

	"tablepro-go/internal/changes"
)

// TableRef names the object a statement writes to.
type TableRef struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
}

var starSelectRe = regexp.MustCompile("(?is)^\\s*select\\s+(?:distinct\\s+)?(?:[\\w.$@#\\[\\]\"`]+\\.)?\\*\\s+from\\s+([\\w.$@#\\[\\]\"`]+)")
var multiTableRe = regexp.MustCompile(`(?is)\b(join|group\s+by|having|union)\b`)

// QuerySource reports the table a statement reads rows from, which is how the
// grid knows it may edit them. The zero ref - no table - means read-only.
func (a *App) QuerySource(connID, query, database string) TableRef {
	conn, err := a.connection(connID)
	if err != nil {
		return TableRef{}
	}
	if database == "" {
		database = conn.Database
	}
	return parseQuerySource(connID, query, database)
}

// parseQuerySource is the text half of that, split out so the shapes can be read
// without a live connection. database is already the fallback to use.
func parseQuerySource(connID, query, database string) TableRef {
	text := sqlCommentRe.ReplaceAllString(query, " ")
	if !isSelectQuery(text) || multiTableRe.MatchString(text) {
		return TableRef{}
	}
	match := starSelectRe.FindStringSubmatch(text)
	if match == nil {
		return TableRef{}
	}
	ref := parseTableRef(match[1])
	if ref.table == "" || !isIdentifierLike(ref.table) {
		return TableRef{}
	}
	if ref.database != "" {
		database = ref.database
	}
	return TableRef{ConnID: connID, Database: database, Schema: ref.schema, Table: ref.table}
}

// numberLiteral is the package's renderer under its historical name.
func numberLiteral(value float64) string { return changes.NumberLiteral(value) }

// SqlName renders a table reference as quoted SQL text for the editor's starter
// statements. Dialect knowledge stays on this side of the boundary.
func (a *App) SqlName(connID, database, schema, table, scope string) (string, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return "", err
	}
	if database == "" {
		database = conn.Database
	}
	if scope == "qualified" {
		return tableName(conn.Type, database, schema, table)
	}
	return tableprosql.LocalName(conn.Type, schema, table)
}

// The shapes the grid sends are the package's shapes.
type (
	CellEdit  = changes.CellEdit
	RowChange = changes.RowChange
	ChangeSet = changes.ChangeSet
)

// How a queued row touches the table.
const (
	rowUpdate = changes.KindUpdate
	rowInsert = changes.KindInsert
	rowDelete = changes.KindDelete
)

// tableName renders the object the way the grid addressed it, so an update
// hits exactly what is on screen.
func tableName(dbType, database, schema, table string) (string, error) {
	return tableprosql.QualifiedTable(dbType, database, schema, table)
}

// defaultSchema is the namespace a table lives in when the caller names none.
func defaultSchema(dbType, schema string) string {
	return tableprosql.DefaultTableSchema(dbType, schema)
}

// quotedLiteral renders a string as a single-quoted SQL literal.
func quotedLiteral(dbType, value string) string {
	return tableprosql.QuotedLiteral(dbType, value)
}

// valueLiteral is the package's renderer under its historical name (filters and
// the update builder share it).
func valueLiteral(dbType, colType string, value interface{}) (string, error) {
	return changes.ValueLiteral(dbType, colType, value)
}

// valuePredicate is the "column = value" test the WHERE clauses are built from.
func valuePredicate(dbType, colType, quotedColumn string, value interface{}) (string, error) {
	return changes.Predicate(dbType, colType, quotedColumn, value)
}

// buildStatements renders the queue; kept as a package-level helper so the tests
// that pin the pure behaviour stay where they were.
func buildStatements(dbType, name string, resultColumns, keyColumns []string, types map[string]string, list []RowChange) ([]string, error) {
	return changes.Build(dbType, name, resultColumns, keyColumns, types, list)
}

// buildUpdateStatements is buildStatements narrowed to edited cells.
func buildUpdateStatements(dbType, name string, resultColumns, keyColumns []string, types map[string]string, list []RowChange) ([]string, error) {
	return changes.BuildUpdates(dbType, name, resultColumns, keyColumns, types, list)
}

// buildChanges resolves what the grid cannot know - the object's name and the
// table's keys and types - and leaves the rest to the pure builder.
func (a *App) buildChanges(set ChangeSet) ([]string, error) {
	conn, err := a.connection(set.ConnID)
	if err != nil {
		return nil, err
	}
	if set.Table == "" {
		return nil, fmt.Errorf("no table to write to")
	}
	schema := defaultSchema(conn.Type, set.Schema)

	name, err := tableName(conn.Type, set.Database, schema, set.Table)
	if err != nil {
		return nil, err
	}
	columns, err := a.GetColumns(set.ConnID, set.Database, schema, set.Table)
	if err != nil {
		return nil, err
	}

	types := make(map[string]string, len(columns))
	var keys []string
	for _, col := range columns {
		types[strings.ToLower(col.Name)] = col.Type
		if col.IsPrimaryKey {
			keys = append(keys, col.Name)
		}
	}
	return changes.Build(conn.Type, name, set.Columns, keys, types, set.Changes)
}

// PreviewChanges is the statement list the user confirms before anything runs.
func (a *App) PreviewChanges(set ChangeSet) ([]string, error) {
	return a.buildChanges(set)
}

// ApplyChanges runs the rebuilt statements as one transaction: either every
// edit lands or none does, which is the only way a mistake stays fixable. A
// statement that matches no row aborts the batch instead of quietly skipping.
func (a *App) ApplyChanges(set ChangeSet) (int64, error) {
	statements, err := a.buildChanges(set)
	if err != nil {
		return 0, err
	}
	// Grid edits already require the ReviewDialog confirmation in the UI, so
	// Safe Mode only has to enforce the hard block here; a "needs confirmation"
	// level is satisfied by that same dialog.
	mode := a.GetSettings().SafeMode
	for _, statement := range statements {
		if guard := guardWrite(mode, statement); guard.Blocked {
			return 0, fmt.Errorf("%s", guard.Reason)
		}
	}
	conn, err := a.connection(set.ConnID)
	if err != nil {
		return 0, err
	}
	drv, err := a.execDriver(conn, set.Database)
	if err != nil {
		return 0, err
	}
	txDrv, ok := drv.(dbcore.SQLTxDriver)
	if !ok {
		return 0, fmt.Errorf("unsupported type: %s", conn.Type)
	}

	ctx := context.Background()
	tx, err := txDrv.Begin(ctx, dbcore.TxMode{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var applied int64
	for i, statement := range statements {
		res, err := txDrv.ExecuteIn(tx, ctx, statement)
		if err != nil {
			return applied, fmt.Errorf("statement %d: %w", i+1, err)
		}
		if res.AffectedRows == 0 {
			return applied, fmt.Errorf("statement %d matched no row; the data changed since it was read, nothing was committed", i+1)
		}
		applied += res.AffectedRows
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return applied, nil
}
