package main

// The filter bar and its WHERE rendering live in internal/filters; the aliases
// keep the call sites (query.go, changes.go, the API) unchanged.

import (
	"tablepro-go/internal/filters"
)

// TableFilter is one condition of the data page's filter bar.
type TableFilter = filters.Condition

// The comparisons the bar offers.
const (
	filterEq       = filters.Eq
	filterNe       = filters.Ne
	filterGT       = filters.GT
	filterGTE      = filters.GTE
	filterLT       = filters.LT
	filterLTE      = filters.LTE
	filterContains = filters.Contains
	filterIsNull   = filters.IsNull
	filterNotNull  = filters.NotNull
)

// tableWhere renders the bar's conditions into the WHERE of the data page's
// query, reading the catalog first.
func (a *App) tableWhere(connID, database, schema, table string, list []TableFilter) (string, error) {
	if len(list) == 0 {
		return "", nil
	}
	conn, err := a.connection(connID)
	if err != nil {
		return "", err
	}
	schema = defaultSchema(conn.Type, schema)
	columns, err := a.GetColumns(connID, database, schema, table)
	if err != nil {
		return "", err
	}
	return tableWhereFrom(conn, table, columns, list)
}

// tableWhereFrom renders the conditions from columns that were read once
// already: a data page needs the same catalog rows for its key ORDER BY and its
// filter WHERE, and used to fetch them twice.
func tableWhereFrom(conn *Connection, table string, columns []ColumnInfo, list []TableFilter) (string, error) {
	return filters.Where(conn.Type, table, columns, list)
}
