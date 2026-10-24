// Package tableprosql is a compatibility facade over dbcore/sqltext.
//
// The implementation moved to github.com/dbcore/dbcore/sqltext so every client
// (TablePro, Fynequery, future consumers) shares one dialect layer. This package
// keeps the historical import path working; new code should import sqltext
// directly.
//
// Deprecated: use github.com/dbcore/dbcore/sqltext.
package tableprosql

import "github.com/dbcore/dbcore/sqltext"

// Engine identifiers, re-exported for callers that still spell them here.
const (
	MySQL     = sqltext.MySQL
	Postgres  = sqltext.Postgres
	SQLite    = sqltext.SQLite
	SQLServer = sqltext.SQLServer
	Oracle    = sqltext.Oracle
)

// Engines lists every identifier the package knows how to generate for.
var Engines = sqltext.Engines

type (
	Dialect    = sqltext.Dialect
	FKRow      = sqltext.FKRow
	ColumnType = sqltext.ColumnType
)

var (
	QuoteIdent           = sqltext.QuoteIdent
	QualifiedName        = sqltext.QualifiedName
	LocalName            = sqltext.LocalName
	QualifiedTable       = sqltext.QualifiedTable
	QuotedLiteral        = sqltext.QuotedLiteral
	RowLimit             = sqltext.RowLimit
	TruncateStatement    = sqltext.TruncateStatement
	RenameStatement      = sqltext.RenameStatement
	FKValuesQuery        = sqltext.FKValuesQuery
	ReferenceAction      = sqltext.ReferenceAction
	CatalogSchema        = sqltext.CatalogSchema
	DefaultTableSchema   = sqltext.DefaultTableSchema
	LikePattern          = sqltext.LikePattern
	NewColumnType        = sqltext.NewColumnType
	CreateTableTemplate  = sqltext.CreateTableTemplate
	DatabasesQuery       = sqltext.DatabasesQuery
	SchemasQuery         = sqltext.SchemasQuery
	TablesQuery          = sqltext.TablesQuery
	ColumnsQuery         = sqltext.ColumnsQuery
	IndexesQuery         = sqltext.IndexesQuery
	ForeignKeysQuery     = sqltext.ForeignKeysQuery
	FullColumnTypesQuery = sqltext.FullColumnTypesQuery
	PreviewPageQuery     = sqltext.PreviewPageQuery
	NamePredicate        = sqltext.NamePredicate
	DialectFor           = sqltext.DialectFor
)
