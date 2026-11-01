package main

// The edit-to-SQL builder lives in internal/changes; this file keeps the App
// wiring (connection lookup, catalog read, transaction) and the type aliases the
// rest of the package (and the JSON API) already uses.

import (
	"context"
	"database/sql"
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

// buildChangePlan is buildChanges with the per-statement metadata kept (kind and
// key predicate), which ApplyChanges needs to interpret a zero row count.
func (a *App) buildChangePlan(set ChangeSet) ([]changes.Statement, error) {
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
	return changes.BuildPlan(conn.Type, name, set.Columns, keys, types, set.Changes)
}

// buildUpdateStatements is buildStatements narrowed to edited cells.
func buildUpdateStatements(dbType, name string, resultColumns, keyColumns []string, types map[string]string, list []RowChange) ([]string, error) {
	return changes.BuildUpdates(dbType, name, resultColumns, keyColumns, types, list)
}

// buildChanges resolves what the grid cannot know - the object's name and the
// table's keys and types - and leaves the rest to the pure builder.
func (a *App) buildChanges(set ChangeSet) ([]string, error) {
	plan, err := a.buildChangePlan(set)
	if err != nil {
		return nil, err
	}
	statements := make([]string, 0, len(plan))
	for _, st := range plan {
		statements = append(statements, st.SQL)
	}
	return statements, nil
}

// PreviewChanges is the statement list the user confirms before anything runs.
func (a *App) PreviewChanges(set ChangeSet) ([]string, error) {
	return a.buildChanges(set)
}

// ApplyChanges runs the rebuilt statements as one transaction: either every
// edit lands or none does, which is the only way a mistake stays fixable. A
// statement that matches no row aborts the batch instead of quietly skipping —
// except an UPDATE that matched its row but changed nothing (MySQL counts only
// changed rows, so it reports zero): the key is probed to tell the two apart.
func (a *App) ApplyChanges(set ChangeSet) (int64, error) {
	plan, err := a.buildChangePlan(set)
	if err != nil {
		return 0, err
	}
	// Grid edits already require the ReviewDialog confirmation in the UI, so
	// Safe Mode only has to enforce the hard block here; a "needs confirmation"
	// level is satisfied by that same dialog.
	mode := a.GetSettings().SafeMode
	for _, statement := range plan {
		if guard := guardWrite(mode, statement.SQL); guard.Blocked {
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

	ctx, done := a.beginRun(set.ConnID)
	defer done()
	tx, err := txDrv.Begin(ctx, dbcore.TxMode{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var applied int64
	for i, statement := range plan {
		res, err := txDrv.ExecuteIn(tx, ctx, statement.SQL)
		if err != nil {
			return applied, fmt.Errorf("statement %d: %w", i+1, err)
		}
		if res.AffectedRows == 0 {
			if verdict := zeroRowVerdict(statement, func() (bool, error) {
				return rowStillThere(txDrv, tx, ctx, statement)
			}); verdict != nil {
				return applied, fmt.Errorf("statement %d: %w", i+1, verdict)
			}
		}
		applied += res.AffectedRows
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// The edit lands as one history entry: the statements the user reviewed in
	// the dialog are the audit trail, and without this the grid path would be
	// invisible next to typed statements.
	statements := make([]string, 0, len(plan))
	for _, st := range plan {
		statements = append(statements, st.SQL)
	}
	a.recordStatement(set.ConnID, set.Database, strings.Join(statements, ";\n"),
		QueryResult{AffectedRows: applied})
	return applied, nil
}

// zeroRowVerdict explains why a statement that affected no rows must abort the
// batch, or returns nil when zero rows is the correct outcome. An UPDATE that
// matched its key but wrote identical values is a no-op on engines that count
// only changed rows (MySQL), so its key is probed first; a DELETE or INSERT
// with zero rows is unambiguous.
func zeroRowVerdict(st changes.Statement, probe func() (bool, error)) error {
	if st.Kind == changes.KindUpdate {
		present, err := probe()
		if err != nil {
			return fmt.Errorf("the key probe for the no-op check failed: %w", err)
		}
		if present {
			return nil // the row is there; the edit simply cancelled out
		}
	}
	return fmt.Errorf("matched no row; the data changed since it was read, nothing was committed")
}

// rowStillThere asks the same transaction whether the statement's key predicate
// still locates a row. The probe runs inside the open transaction, so it sees
// exactly the state the UPDATE or DELETE was applied to.
func rowStillThere(txDrv dbcore.SQLTxDriver, tx *sql.Tx, ctx context.Context, st changes.Statement) (bool, error) {
	probe := fmt.Sprintf("SELECT 1 FROM %s WHERE %s", st.Table, st.Where)
	res, err := txDrv.ExecuteIn(tx, ctx, probe)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}
