package main

import (
	"tableprosql"

	"fmt"
	"strings"
	"time"
)

// ForeignKeyValue is one choice for a foreign-key cell: the key the cell would
// store, plus a short description of the row behind it, so "7" and "12" can be
// told apart without opening the other table.
type ForeignKeyValue struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ForeignKeyCandidates answers "what could this cell hold". RefTable/RefColumn
// name the object the list was read from, which the grid shows above the list.
type ForeignKeyCandidates struct {
	RefTable    string            `json:"refTable"`
	RefColumn   string            `json:"refColumn"`
	LabelColumn string            `json:"labelColumn"`
	Values      []ForeignKeyValue `json:"values"`
	Truncated   bool              `json:"truncated"`
}

const (
	fkValuesDefaultLimit = 200
	fkValuesMaxLimit     = 500
)

// singleColumnForeignKey finds the constraint that makes column a reference to
// exactly one column of one other table. A composite key is refused on purpose:
// one cell cannot hold half a key, so the grid keeps plain text entry there.
func singleColumnForeignKey(fks []ForeignKeyInfo, column string) (ForeignKeyInfo, error) {
	for _, fk := range fks {
		matched := false
		for _, name := range fk.Columns {
			if strings.EqualFold(name, column) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 {
			return fk, nil
		}
		return ForeignKeyInfo{}, fmt.Errorf(
			"%s 是复合外键 %s 的一部分，单元格只能自己填值", column, strings.Join(fk.Columns, "+"))
	}
	return ForeignKeyInfo{}, fmt.Errorf("%s 没有外键", column)
}

// fkTargetName renders the referenced object the way this dialect addresses
// tables, reusing the name builder the update statements use so the list is read
// from the very object an edit would write.
func fkTargetName(dbType, database, refSchema, refTable string) (string, error) {
	if dbType == "mysql" {
		// In MySQL's catalog the "schema" of a reference is a database name.
		if refSchema == "" {
			refSchema = database
		}
		return tableName(dbType, refSchema, "", refTable)
	}
	return tableName(dbType, database, defaultSchema(dbType, refSchema), refTable)
}

// isDescriptiveType reports whether a column holds something a person reads.
// Numbers, dates and blobs describe a row only to whoever already knows it.
// The type knowledge lives in tableprosql.ColumnType.
func isDescriptiveType(colType string) bool {
	return tableprosql.NewColumnType(colType).IsDescriptive()
}

// pickLabelColumn chooses the description column: the first readable text column
// that is neither the key being stored nor part of any key. A table with nothing
// like that gets no description at all rather than an id echoing itself.
func pickLabelColumn(columns []ColumnInfo, refColumn string) string {
	for _, col := range columns {
		if col.IsPrimaryKey || strings.EqualFold(col.Name, refColumn) {
			continue
		}
		if isDescriptiveType(col.Type) {
			return col.Name
		}
	}
	return ""
}

// fkValuesQuery reads the distinct keys of the referenced table. Ordering on the
// key is what makes consecutive reads agree, and DISTINCT only allows it because
// the key is in the select list.
func fkValuesQuery(dbType, name, refColumn, labelColumn string, limit int) (string, error) {
	return tableprosql.FKValuesQuery(dbType, name, refColumn, labelColumn, limit)
}

// candidateText renders a key the way the grid shows it, so the text picked from
// the list is the text the cell then holds.
func candidateText(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return numberLiteral(v)
	case bool:
		if v {
			return "1"
		}
		return "0"
	}
	return fmt.Sprintf("%v", value)
}

// ForeignKeys lists the constraints a table has, so the grid can show a
// dropdown arrow on the cells that hold a reference before anyone clicks one.
func (a *App) ForeignKeys(connID, database, schema, table string) ([]ForeignKeyInfo, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}
	if schema == "" {
		schema = resolveSchema(conn, database, schema)
	}
	return a.tableForeignKeys(conn, database, schema, table)
}

// ForeignKeyValues lists the values a foreign-key cell may take. It reads the
// catalog first, so a column that is not a single-column reference is refused
// before anything is queried.
func (a *App) ForeignKeyValues(connID, database, schema, table, column string, limit int) (ForeignKeyCandidates, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}
	if limit <= 0 || limit > fkValuesMaxLimit {
		limit = fkValuesDefaultLimit
	}
	if schema == "" {
		schema = resolveSchema(conn, database, schema)
	}

	fks, err := a.tableForeignKeys(conn, database, schema, table)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}
	fk, err := singleColumnForeignKey(fks, column)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}
	refColumn := fk.RefColumns[0]

	name, err := fkTargetName(conn.Type, database, fk.RefSchema, fk.RefTable)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}

	refSchema := fk.RefSchema
	if conn.Type == "mysql" && refSchema == "" {
		refSchema = database
	}
	// The description is a convenience: a catalog the database filter hides must
	// not cost the user their list of keys.
	var labelColumn string
	if columns, err := a.GetColumns(connID, database, refSchema, fk.RefTable); err == nil {
		labelColumn = pickLabelColumn(columns, refColumn)
	}

	query, err := fkValuesQuery(conn.Type, name, refColumn, labelColumn, limit)
	if err != nil {
		return ForeignKeyCandidates{}, err
	}

	db := conn.DB
	if conn.Type == "postgresql" {
		if db, err = a.poolFor(conn, database); err != nil {
			return ForeignKeyCandidates{}, err
		}
	}
	result := runSelect(db, query, time.Now())
	if result.Error != "" {
		return ForeignKeyCandidates{}, fmt.Errorf("%s", result.Error)
	}

	values := make([]ForeignKeyValue, 0, result.RowCount)
	seen := map[string]bool{}
	for _, row := range result.Rows {
		value := candidateText(row[0])
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		label := ""
		if len(row) > 1 {
			label = candidateText(row[1])
		}
		values = append(values, ForeignKeyValue{Value: value, Label: label})
	}

	return ForeignKeyCandidates{
		RefTable:    fk.RefTable,
		RefColumn:   refColumn,
		LabelColumn: labelColumn,
		Values:      values,
		Truncated:   result.RowCount >= limit,
	}, nil
}
