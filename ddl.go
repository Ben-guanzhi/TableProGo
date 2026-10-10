package main

import (
	"tableprosql"

	"fmt"
	"strings"

	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"
)

func ddlIdent(dbType, part string) string {
	quoted, err := quoteIdent(dbType, part)
	if err != nil {
		return part
	}
	return quoted
}

func ddlColumns(dbType string, names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, ddlIdent(dbType, name))
	}
	return strings.Join(quoted, ", ")
}

// ddlTableName qualifies only when a schema exists, because SQLite has no
// schema argument in the tree and "main" would be noise in the DDL.
func ddlTableName(dbType, schema, table string) string {
	if schema == "" || schema == "main" {
		return ddlIdent(dbType, table)
	}
	return ddlIdent(dbType, schema) + "." + ddlIdent(dbType, table)
}

// buildCreateTable reconstructs the DDL from catalog rows. It is a reading aid
// rather than a migration tool, so engine-level storage clauses are left out.
func buildCreateTable(dbType, schema, table string, columns []ColumnInfo, types map[string]string, indexes []IndexInfo, foreignKeys []ForeignKeyInfo) string {
	object := ddlTableName(dbType, schema, table)

	definitions := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		dataType := column.Type
		if full, ok := types[column.Name]; ok && full != "" {
			dataType = full
		}
		line := "  " + ddlIdent(dbType, column.Name) + " " + dataType
		if value := strings.TrimSpace(column.DefaultValue); value != "" {
			line += " DEFAULT " + normalizeDefault(dbType, value)
		}
		if !column.Nullable {
			line += " NOT NULL"
		}
		definitions = append(definitions, line)
	}

	statements := []string{}
	for _, index := range indexes {
		if !index.Primary {
			continue
		}
		definitions = append(definitions,
			"  CONSTRAINT "+ddlIdent(dbType, index.Name)+" PRIMARY KEY ("+ddlColumns(dbType, index.Columns)+")")
	}
	statements = append(statements, "CREATE TABLE "+object+" (\n"+strings.Join(definitions, ",\n")+"\n);")

	for _, index := range indexes {
		if index.Primary || len(index.Columns) == 0 {
			continue
		}
		unique := ""
		if index.Unique {
			unique = "UNIQUE "
		}
		statements = append(statements, fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);",
			unique, ddlIdent(dbType, index.Name), object, ddlColumns(dbType, index.Columns)))
	}

	for _, fk := range foreignKeys {
		referenced := ddlTableName(dbType, fk.RefSchema, fk.RefTable)
		line := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)",
			object, ddlIdent(dbType, fk.Name), ddlColumns(dbType, fk.Columns), referenced, ddlColumns(dbType, fk.RefColumns))
		if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
			line += " ON DELETE " + fk.OnDelete
		}
		if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
			line += " ON UPDATE " + fk.OnUpdate
		}
		statements = append(statements, line+";")
	}

	return strings.Join(statements, "\n\n")
}

// normalizeDefault unwraps the parentheses MSSQL wraps around every default and
// trims the padding Oracle adds to expression text.
func normalizeDefault(dbType, value string) string {
	if dbType == "sqlserver" {
		for strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
			inner := strings.TrimSpace(value[1 : len(value)-1])
			if inner == "" {
				break
			}
			value = inner
		}
	}
	return value
}

// GetCreateTableTemplate renders the starter DDL for "新建表"; the dialects
// live in the shared SQL text package so the webview and the Fyne client
// open the editor on the same template.
func (a *App) GetCreateTableTemplate(dbType, schema string) string {
	return tableprosql.CreateTableTemplate(dbType, schema)
}
