package main

import (
	"github.com/dbcore/dbcore/ddl"
	tableprosql "github.com/dbcore/dbcore/sqltext"

	"tablepro-go/internal/catalogmodel"
)

// buildCreateTable reconstructs the DDL from catalog rows. It is a reading aid
// rather than a migration tool, so engine-level storage clauses are left out.
// The assembly and the per-engine default normalization live in the shared
// kernel (dbcore/ddl); this file only maps our catalog shapes onto its inputs
// — no DDL dialect knowledge may grow here (INTEGRATION-PLAN P1-7).
func buildCreateTable(dbType, schema, table string, columns []catalogmodel.ColumnInfo, types map[string]string, indexes []catalogmodel.IndexInfo, foreignKeys []catalogmodel.ForeignKeyInfo) string {
	cols := make([]ddl.Column, len(columns))
	for i, column := range columns {
		cols[i] = ddl.Column{
			Name:         column.Name,
			Type:         column.Type,
			Nullable:     column.Nullable,
			DefaultValue: column.DefaultValue,
		}
	}
	idxs := make([]ddl.Index, len(indexes))
	for i, index := range indexes {
		idxs[i] = ddl.Index{
			Name:    index.Name,
			Unique:  index.Unique,
			Primary: index.Primary,
			Columns: index.Columns,
		}
	}
	fks := make([]ddl.ForeignKey, len(foreignKeys))
	for i, fk := range foreignKeys {
		fks[i] = ddl.ForeignKey{
			Name:       fk.Name,
			Columns:    fk.Columns,
			RefSchema:  fk.RefSchema,
			RefTable:   fk.RefTable,
			RefColumns: fk.RefColumns,
			OnDelete:   fk.OnDelete,
			OnUpdate:   fk.OnUpdate,
		}
	}
	return ddl.CreateTable(dbType, schema, table, cols, types, idxs, fks)
}

// normalizeDefault unwraps the parentheses MSSQL wraps around every default and
// trims the padding Oracle adds to expression text — kernel-owned dialect
// knowledge, re-exported here for the structure page's readable column list.
func normalizeDefault(dbType, value string) string {
	return ddl.NormalizeDefault(dbType, value)
}

// GetCreateTableTemplate renders the starter DDL for "新建表"; the dialects
// live in the shared SQL text package so the webview and the Fyne client
// open the editor on the same template.
func (a *App) GetCreateTableTemplate(dbType, schema string) string {
	return tableprosql.CreateTableTemplate(dbType, schema)
}
