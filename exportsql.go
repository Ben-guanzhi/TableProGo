package main

import (
	"fmt"
	"strings"
)

// insertExportMaxRows bounds what one export may render. The grid only ever
// holds a page of the table, so hitting this means the client sent something it
// should not have - refusing is better than a script that quietly misses rows.
const insertExportMaxRows = 10000

// insertScript renders one INSERT per row, with every literal written the way a
// save writes it: the column's declared type decides whether "7" is a number or
// text, and the dialect decides the quoting. Values are matched to columns by
// position, which is the only mapping a result that came out of this table has.
func insertScript(
	dbType string,
	name string,
	table string,
	resultColumns []string,
	types map[string]string,
	rows [][]interface{},
) (string, error) {
	idents := make([]string, len(resultColumns))
	colTypes := make([]string, len(resultColumns))
	seen := make(map[string]bool, len(resultColumns))
	for i, col := range resultColumns {
		lower := strings.ToLower(col)
		if seen[lower] {
			return "", fmt.Errorf("结果里有同名列 %s，无法确定该写入哪个字段", col)
		}
		seen[lower] = true
		colType, ok := types[lower]
		if !ok {
			return "", fmt.Errorf(
				"结果列 %s 不是表 %s 的字段（别名或计算列还原不出 INSERT）", col, table)
		}
		ident, err := quoteIdent(dbType, col)
		if err != nil {
			return "", err
		}
		idents[i] = ident
		colTypes[i] = colType
	}

	statements := make([]string, 0, len(rows))
	for i, row := range rows {
		if len(row) != len(resultColumns) {
			return "", fmt.Errorf("第 %d 行有 %d 个值，和 %d 列对不上", i+1, len(row), len(resultColumns))
		}
		values := make([]string, len(row))
		for j, value := range row {
			literal, err := valueLiteral(dbType, colTypes[j], value)
			if err != nil {
				return "", fmt.Errorf("第 %d 行的 %s：%w", i+1, resultColumns[j], err)
			}
			values[j] = literal
		}
		statements = append(statements, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);",
			name, strings.Join(idents, ", "), strings.Join(values, ", ")))
	}
	return strings.Join(statements, "\n"), nil
}

// InsertScript turns the rows on screen into a script that recreates them. It
// reads the catalog for the same table a save would write to, so a result that
// is not backed by one table is refused rather than guessed at.
func (a *App) InsertScript(connID, database, schema, table string, columns []string, rows [][]interface{}) (string, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return "", err
	}
	if table == "" {
		return "", fmt.Errorf("当前结果没有来源表，无法生成 INSERT")
	}
	if len(columns) == 0 {
		return "", fmt.Errorf("当前结果没有列，无法生成 INSERT")
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("当前结果为空，没有可导出的行")
	}
	if len(rows) > insertExportMaxRows {
		return "", fmt.Errorf("一次最多导出 %d 行，请翻页或缩小范围", insertExportMaxRows)
	}
	schema = defaultSchema(conn.Type, schema)

	name, err := tableName(conn.Type, database, schema, table)
	if err != nil {
		return "", err
	}
	tableColumns, err := a.GetColumns(connID, database, schema, table)
	if err != nil {
		return "", err
	}
	types := make(map[string]string, len(tableColumns))
	for _, col := range tableColumns {
		types[strings.ToLower(col.Name)] = col.Type
	}
	return insertScript(conn.Type, name, table, columns, types, rows)
}
