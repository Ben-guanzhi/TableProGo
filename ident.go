package main

import (
	"tableprosql"

	"regexp"
	"strings"

	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"
)

// sqlCommentRe strips comments so a commented-out "FROM x" cannot be mistaken
// for the query's real table.
var sqlCommentRe = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)

// queryTableRe grabs the first object named in a FROM/JOIN/UPDATE/INTO clause.
var queryTableRe = regexp.MustCompile("(?is)\\b(?:from|join|update|into)\\s+([\\w.$@#\\[\\]\"`]+)")

// tableRef is a table name as written in a query, with each part unquoted.
type tableRef struct {
	database string
	schema   string
	table    string
}

func unquoteIdentPart(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}
	if len(part) >= 2 && part[0] == '[' && part[len(part)-1] == ']' {
		return strings.ReplaceAll(part[1:len(part)-1], "]]", "]")
	}
	quote := part[0]
	if len(part) >= 2 && (quote == '`' || quote == '"' || quote == '\'') && part[len(part)-1] == quote {
		return strings.ReplaceAll(part[1:len(part)-1], string([]byte{quote, quote}), string(quote))
	}
	return part
}

func parseTableRef(raw string) tableRef {
	parts := strings.Split(strings.Trim(raw, " \t\r\n"), ".")
	for i, part := range parts {
		parts[i] = unquoteIdentPart(part)
	}

	var ref tableRef
	switch len(parts) {
	case 1:
		ref.table = parts[0]
	case 2:
		ref.schema, ref.table = parts[0], parts[1]
	default:
		ref.database, ref.schema, ref.table = parts[len(parts)-3], parts[len(parts)-2], parts[len(parts)-1]
	}
	return ref
}

func normalizeTableType(raw string) string {
	if strings.Contains(strings.ToUpper(raw), "VIEW") {
		return "view"
	}
	return "table"
}

// quoteIdent safely quotes an identifier for each engine; the shapes live in
// the shared SQL text package both TablePro clients generate from.
func quoteIdent(dbType string, name string) (string, error) {
	return tableprosql.QuoteIdent(dbType, name)
}

func qualifiedName(dbType string, parts ...string) (string, error) {
	return tableprosql.QualifiedName(dbType, parts...)
}

func isIdentifierLike(name string) bool {
	for i, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return name != ""
}
