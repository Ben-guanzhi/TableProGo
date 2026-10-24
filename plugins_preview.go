package main

// The page a plugin-backed connection shows. The plugin renders the statement
// itself, so the client sends the parts (sort, filter) and receives rows.
//
// The filters are the client's own filter bar, which names a column and a
// comparison — never SQL. They are translated into the kernel's filter
// expressions here, and the plugin quotes them for its own dialect.

import (
	"context"
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
)

// previewViaPlugin reads one page through the plugin's BrowseRows.
func (a *App) previewViaPlugin(conn *Connection, database, schema, table string, limit, offset int, filters []TableFilter) QueryResult {
	drv, err := a.pluginDriver(conn)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	if schema == "" {
		schema = defaultSchema(conn.Type, schema)
	}

	req := &dbcore.TableRowsRequest{
		Page:     offset / maxInt(limit, 1),
		PageSize: limit,
	}
	// The filter bar's conditions become kernel filter expressions. A condition the
	// plugin cannot satisfy (a column it does not have) is an error, not a silently
	// dropped filter: the page would then show rows the user did not ask for.
	for _, filter := range filters {
		expr, err := pluginFilter(filter)
		if err != nil {
			return QueryResult{Error: err.Error()}
		}
		req.Filter = append(req.Filter, expr)
	}

	browsed, err := drv.BrowseRows(context.Background(), schema, table, req)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	out := QueryResult{RowCount: len(browsed.Rows)}
	for _, col := range browsed.Columns {
		out.Columns = append(out.Columns, Column{Name: col.Name, Type: col.Type, Nullable: col.Nullable})
	}
	for _, row := range browsed.Rows {
		cells := make([]interface{}, len(row))
		for i, cell := range row {
			cells[i] = cellValue(cell)
		}
		out.Rows = append(out.Rows, cells)
	}
	return out
}

// pluginFilter converts one bar condition into a kernel filter expression. The
// column name travels as written; the plugin quotes it, which is what keeps a
// column called "order" from breaking the statement.
func pluginFilter(filter TableFilter) (dbcore.FilterExpr, error) {
	op, ok := pluginFilterOp(filter.Op)
	if !ok {
		return dbcore.FilterExpr{}, fmt.Errorf("unknown filter operator %q", filter.Op)
	}
	column := strings.TrimSpace(filter.Column)
	if column == "" {
		return dbcore.FilterExpr{}, fmt.Errorf("a filter needs a column")
	}
	value := dbcore.Value{Kind: dbcore.ValText, S: filter.Value}
	if filter.Op == filterIsNull || filter.Op == filterNotNull {
		value = dbcore.Value{Kind: dbcore.ValNull}
	}
	return dbcore.FilterExpr{Column: column, Op: op, Value: value}, nil
}

// pluginFilterOp maps the bar's operators onto the kernel's.
func pluginFilterOp(op string) (dbcore.FilterOp, bool) {
	switch op {
	case filterEq:
		return dbcore.FilterEq, true
	case filterNe:
		return dbcore.FilterNeq, true
	case filterGT:
		return dbcore.FilterGt, true
	case filterGTE:
		return dbcore.FilterGte, true
	case filterLT:
		return dbcore.FilterLt, true
	case filterLTE:
		return dbcore.FilterLte, true
	case filterContains:
		return dbcore.FilterLike, true
	case filterIsNull:
		return dbcore.FilterIsNull, true
	case filterNotNull:
		return dbcore.FilterIsNotNull, true
	}
	return "", false
}

// maxInt is the larger of two ints; Go 1.21 has no builtin for it in this module.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
