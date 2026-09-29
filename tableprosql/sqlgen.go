// Package tableprosql is the single home of the SQL *text* both TablePro
// clients (the Wails root and the Fyne client) generate: identifier quoting,
// literals, row limiting, paging, table actions and the FK-candidate query.
// It is deliberately dependency-free — every function maps strings to
// strings — so both go modules can share one copy without unifying their
// result/execution types. Catalog *readers* (which scan rows into each
// client's own structs) stay with the clients.
//
// The engine identifier is the type tag; each function is the op side of the
// op-and-type table. Adding an engine means touching the switch arms below,
// not the callers.
package tableprosql

import (
	"fmt"
	"regexp"
	"strings"
)

// Engine identifiers, in the exact spelling both clients persist in their
// connection configs.
const (
	MySQL     = "mysql"
	Postgres  = "postgresql"
	SQLite    = "sqlite"
	SQLServer = "sqlserver"
	Oracle    = "oracle"
)

// Engines lists every identifier the package knows how to generate for.
var Engines = []string{MySQL, Postgres, SQLite, SQLServer, Oracle}

// QuoteIdent quotes an identifier the way the engine parses it: MySQL wants
// backticks, SQL Server brackets, everything else the SQL standard's double
// quotes. Overlong or control-carrying names are rejected outright — they
// cannot be quoted safely and would only fail at the engine anyway.
func QuoteIdent(dbType, name string) (string, error) {
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n") {
		return "", fmt.Errorf("invalid identifier: %q", name)
	}
	switch dbType {
	case MySQL:
		return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
	case SQLServer:
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]", nil
	case Postgres, SQLite, Oracle:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
	}
	return "", fmt.Errorf("unsupported type: %s", dbType)
}

// QualifiedName quotes each part and joins them with the engine's qualifier.
func QualifiedName(dbType string, parts ...string) (string, error) {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		q, err := QuoteIdent(dbType, part)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, q)
	}
	return strings.Join(quoted, "."), nil
}

// QuotedLiteral renders a string as a single-quoted SQL literal. It serves
// every place the program inlines text — grid values, LIKE patterns, and
// string-argument positions such as sp_rename's parameters. Only SQL Server
// needs the national prefix to keep wide characters, and PostgreSQL rejects
// it, so the rest get a plain single-quoted literal.
func QuotedLiteral(dbType, value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	if dbType == SQLServer {
		return "N'" + escaped + "'"
	}
	return "'" + escaped + "'"
}

// ownLimitRe spots SQL that already caps its own rows. A false positive only
// means the statement runs untouched, which is the safer direction.
var ownLimitRe = regexp.MustCompile(`(?i)\b(top|limit|offset|fetch|rownum)\b`)

// RowLimit caps a plain SELECT — or a WITH ... SELECT CTE — the way GUI
// clients do: TOP for SQL Server, LIMIT for MySQL/PostgreSQL/SQLite, a ROWNUM
// wrapper for Oracle. Statements with their own cap, and anything that is not
// a SELECT, run untouched.
func RowLimit(dbType, query string, limit int) string {
	if limit <= 0 {
		return query
	}
	trimmed := strings.TrimRight(strings.TrimSpace(query), ";")
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return query
	}
	if ownLimitRe.MatchString(trimmed) {
		return query
	}
	switch dbType {
	case SQLServer:
		// TOP can only be injected into a bare SELECT; a CTE keeps whatever
		// limit it declared itself.
		if !strings.HasPrefix(upper, "SELECT") {
			return query
		}
		return fmt.Sprintf("SELECT TOP (%d)%s", limit, trimmed[len("SELECT"):])
	case Oracle: // ROWNUM wrap is valid around a CTE too
		return fmt.Sprintf("SELECT * FROM (%s) WHERE ROWNUM <= %d", trimmed, limit)
	default: // mysql, postgresql, sqlite — trailing LIMIT binds the whole statement
		return fmt.Sprintf("%s LIMIT %d", trimmed, limit)
	}
}

// Paging is deliberately NOT shared here: the Fyne client pairs the window
// clause with a pageability guard (own-window refusal, page/size bounds) that
// is client-level policy, and the root client builds pages inline around its
// own ORDER BY handling. The raw window renderers stay with the clients.

// TruncateStatement empties a table. SQLite has no TRUNCATE; deleting every
// row is what its own CLI does, and it keeps the sqlite_sequence counter
// alone. `quotedTable` must already carry the engine's quoting — the caller
// built it through QualifiedName/QuoteIdent with its own namespace rules.
func TruncateStatement(dbType, quotedTable string) (string, error) {
	switch dbType {
	case MySQL, Postgres, SQLServer, Oracle:
		return "TRUNCATE TABLE " + quotedTable, nil
	case SQLite:
		return "DELETE FROM " + quotedTable, nil
	}
	return "", fmt.Errorf("unsupported type: %s", dbType)
}

// RenameStatement renders a table rename. `current` and `next` are the fully
// qualified old and new names as the caller's namespace rules dictate (MySQL
// names both sides in full so the table cannot drift into another database on
// the way). Only statement shape lives here; name validation stays with the
// caller, which owns the user-facing error text.
func RenameStatement(dbType, current, next, schema, oldTable, newName string) (string, error) {
	switch dbType {
	case MySQL:
		return fmt.Sprintf("RENAME TABLE %s TO %s", current, next), nil
	case SQLServer:
		// sp_rename takes text, not identifiers: brackets in the string mean
		// an escaped name rather than a quoted one.
		return fmt.Sprintf("EXEC sp_rename %s, %s",
			QuotedLiteral(dbType, DefaultTableSchema(dbType, schema)+"."+oldTable),
			QuotedLiteral(dbType, newName)), nil
	case Postgres, Oracle, SQLite:
		quotedNew, err := QuoteIdent(dbType, newName)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ALTER TABLE %s RENAME TO %s", current, quotedNew), nil
	}
	return "", fmt.Errorf("unsupported type: %s", dbType)
}

// FKValuesQuery renders the DISTINCT candidate-keys query for an FK dropdown.
// Ordering on the key is what makes consecutive reads agree, and DISTINCT
// only allows it because the key is in the select list. `name` must already
// be qualified and quoted by the caller.
func FKValuesQuery(dbType, name, refColumn, labelColumn string, limit int) (string, error) {
	if limit <= 0 {
		return "", fmt.Errorf("invalid limit %d", limit)
	}
	key, err := QuoteIdent(dbType, refColumn)
	if err != nil {
		return "", err
	}
	selected := key
	if labelColumn != "" {
		label, err := QuoteIdent(dbType, labelColumn)
		if err != nil {
			return "", err
		}
		selected += ", " + label
	}
	switch dbType {
	case MySQL, Postgres, SQLite:
		return fmt.Sprintf("SELECT DISTINCT %s FROM %s ORDER BY %s LIMIT %d", selected, name, key, limit), nil
	case SQLServer, Oracle:
		return fmt.Sprintf(
			"SELECT DISTINCT %s FROM %s ORDER BY %s OFFSET 0 ROWS FETCH NEXT %d ROWS ONLY",
			selected, name, key, limit), nil
	}
	return "", fmt.Errorf("unsupported type: %s", dbType)
}

// FKRow is the flat, one-row-per-column shape the FK catalogs return for a
// composite key; the aggregated constraint lists are what the UI and the DDL
// want. Each client scans its engine's catalog rows into one FKRow per column
// pair, then folds the rows into its own constraint type.
type FKRow struct {
	Name      string
	Column    string
	RefSchema string
	RefTable  string
	RefColumn string
	OnDelete  string
	OnUpdate  string
}

// ReferenceAction renders a catalog reference action ("no_action", "CASCADE",
// "RESTRICT") the way it is written in DDL: upper case, whitespace trimmed,
// underscores as spaces.
func ReferenceAction(raw string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(raw)), "_", " ")
}

// CatalogSchema names the namespace catalog queries run against inside one
// database. Engines without a schema layer answer with the database itself;
// callers only ask while the schema is still empty.
func CatalogSchema(dbType, database, schema string) string {
	switch dbType {
	case MySQL:
		return database
	case Postgres:
		return "public"
	case SQLServer:
		return "dbo"
	default: // sqlite, oracle — the schema stands for itself / the connected user
		return schema
	}
}

// DefaultTableSchema fills the engine's default table namespace for qualified
// names, independent of which database the session is in.
func DefaultTableSchema(dbType, schema string) string {
	if schema != "" {
		return schema
	}
	switch dbType {
	case Postgres:
		return "public"
	case SQLServer:
		return "dbo"
	}
	return schema
}

// LikePattern wraps the fragment for a LIKE test, escaping the wildcards the
// user typed so "a_b" cannot stand for any three characters.
func LikePattern(fragment string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(fragment)
	return "%" + escaped + "%"
}

// numericTypeNames is the closed set of declared column-type names that mean
// "this column holds a number". Every client that renders values against a
// declared type (grid edits, filters, exports) matches through
// IsNumericColumnType, so a new spelling (say, another engine's synonym for
// BIGINT) is added here once instead of per client.
var numericTypeNames = map[string]bool{
	"int": true, "integer": true, "int2": true, "int4": true, "int8": true,
	"tinyint": true, "smallint": true, "mediumint": true, "bigint": true,
	"serial": true, "bigserial": true, "smallserial": true,
	"decimal": true, "dec": true, "numeric": true, "fixed": true,
	"float": true, "float4": true, "float8": true, "double": true,
	"double precision": true, "real": true, "money": true, "smallmoney": true,
	"number": true, "binary_float": true, "binary_double": true, "year": true,
}

// IsNumericColumnType reports whether a declared column type holds numbers.
// The type is read the way every catalog spells it: any parenthesized
// precision is dropped, surrounding case and padding ignored, and a trailing
// " unsigned" (MySQL's int family) removed before the name is matched against
// the set. An unrecognized name is not numeric — a value for it stays text.
func IsNumericColumnType(colType string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.Split(colType, "(")[0]))
	return numericTypeNames[strings.TrimSuffix(base, " unsigned")]
}

// CreateTableTemplate renders the starter CREATE TABLE for "新建表": the
// smallest table this engine accepts, so the user edits a name and a column
// instead of recalling each dialect's identity syntax. `schema` fills the
// engine's own default namespace.
func CreateTableTemplate(dbType, schema string) string {
	switch dbType {
	case MySQL:
		// MySQL addresses tables by database, which the session already
		// carries, so the template names the table bare.
		return strings.Join([]string{
			"CREATE TABLE `new_table` (",
			"  `id` INT NOT NULL AUTO_INCREMENT,",
			"  `name` VARCHAR(255) NULL,",
			"  `created_at` DATETIME NULL,",
			"  PRIMARY KEY (`id`)",
			") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;",
		}, "\n")
	case Postgres:
		return strings.Join([]string{
			"CREATE TABLE " + mustQuote(dbType, DefaultTableSchema(dbType, schema)) + `."new_table" (`,
			"  id BIGSERIAL PRIMARY KEY,",
			"  name TEXT,",
			"  created_at TIMESTAMPTZ",
			");",
		}, "\n")
	case SQLServer:
		return strings.Join([]string{
			"CREATE TABLE " + mustQuote(dbType, DefaultTableSchema(dbType, schema)) + ".[new_table] (",
			"  [id] INT IDENTITY(1,1) NOT NULL PRIMARY KEY,",
			"  [name] NVARCHAR(255) NULL,",
			"  [created_at] DATETIME2 NULL",
			");",
		}, "\n")
	case Oracle:
		// Oracle's upper-case identifier convention, and the identity column
		// that makes a bare row insertable.
		return strings.Join([]string{
			`CREATE TABLE "NEW_TABLE" (`,
			`  "ID" NUMBER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,`,
			`  "NAME" VARCHAR2(255),`,
			`  "CREATED_AT" TIMESTAMP`,
			");",
		}, "\n")
	case SQLite:
		return strings.Join([]string{
			`CREATE TABLE "new_table" (`,
			`  "id" INTEGER PRIMARY KEY AUTOINCREMENT,`,
			`  "name" TEXT,`,
			`  "created_at" TEXT`,
			");",
		}, "\n")
	}
	return ""
}

// mustQuote is QuoteIdent for template contexts: the schema comes from the
// catalog, and a name the quoter rejects would have failed long before the
// template is rendered. The empty string keeps the bare-table engines bare.
func mustQuote(dbType, name string) string {
	if name == "" {
		return ""
	}
	q, err := QuoteIdent(dbType, name)
	if err != nil {
		return name
	}
	return q
}
