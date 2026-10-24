// Package filters renders the data page's filter bar into a WHERE clause.
//
// The bar can name a column but never write SQL: the clause is rebuilt here from
// catalog rows, the same way an update is, which is what lets a filtered page
// still be edited and written back to the rows it shows.
package filters

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore/sqltext"

	"tablepro-go/internal/catalogmodel"
)

// Condition is one condition of the filter bar: a column, how it is compared,
// and what it is compared with.
type Condition struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

// The comparisons the bar offers. Each is spelled out below; anything else is
// refused rather than guessed at.
const (
	Eq       = "eq"
	Ne       = "ne"
	GT       = "gt"
	GTE      = "gte"
	LT       = "lt"
	LTE      = "lte"
	Contains = "contains"
	IsNull   = "null"
	NotNull  = "notNull"
)

// likeEscape is the ESCAPE character of a contains test. Backslash would give
// the pattern a second meaning inside a MySQL string literal, so the wildcards
// are escaped with a character no engine treats specially.
const likeEscape = "/"

// Where renders the bar's conditions for table. Columns are matched against the
// catalog, ignoring case, so the only names that reach a statement are ones the
// table actually has. Conditions are ANDed: the bar narrows the page, and a row
// has to satisfy every condition on it.
func Where(dbType string, table string, columns []catalogmodel.ColumnInfo, conditions []Condition) (string, error) {
	if len(conditions) == 0 {
		return "", nil
	}
	byName := make(map[string]catalogmodel.ColumnInfo, len(columns))
	for _, col := range columns {
		byName[strings.ToLower(col.Name)] = col
	}

	rendered := make([]string, 0, len(conditions))
	for _, filter := range conditions {
		col, ok := byName[strings.ToLower(strings.TrimSpace(filter.Column))]
		if !ok {
			return "", fmt.Errorf("%s is not a column of %s", filter.Column, table)
		}
		quoted, err := sqltext.QuoteIdent(dbType, col.Name)
		if err != nil {
			return "", err
		}
		condition, err := render(dbType, col.Type, quoted, filter)
		if err != nil {
			return "", fmt.Errorf("%s: %w", col.Name, err)
		}
		rendered = append(rendered, condition)
	}
	return " WHERE " + strings.Join(rendered, " AND "), nil
}

// render builds one condition. colType is the column's declared type, which
// decides whether a typed value is a number or a string.
func render(dbType, colType, quoted string, f Condition) (string, error) {
	switch f.Op {
	case IsNull:
		return quoted + " IS NULL", nil
	case NotNull:
		return quoted + " IS NOT NULL", nil
	case Contains:
		if sqltext.NewColumnType(colType).IsNumeric() {
			return "", fmt.Errorf("“包含”不适用于 %s 列，改用等于或大小比较", colType)
		}
		return quoted + " LIKE " + sqltext.QuotedLiteral(dbType, containsPattern(f.Value)) +
			" ESCAPE " + sqltext.QuotedLiteral(dbType, likeEscape), nil
	case Eq, Ne, GT, GTE, LT, LTE:
	default:
		return "", fmt.Errorf("unknown filter operator %q", f.Op)
	}

	literal, err := valueLiteral(dbType, colType, f.Value)
	if err != nil {
		return "", err
	}
	// An empty value in a numeric column is NULL, and "col > NULL" is neither
	// true nor false - it silently matches nothing. Only equality can mean an
	// absent value, so the rest are refused while empty.
	if literal == "NULL" {
		switch f.Op {
		case Eq:
			return quoted + " IS NULL", nil
		case Ne:
			return quoted + " IS NOT NULL", nil
		}
		return "", fmt.Errorf("这个比较需要一个值，否则不会匹配任何行")
	}
	return quoted + " " + comparison(f.Op) + " " + literal, nil
}

// containsPattern turns what the user typed into a LIKE pattern: the characters
// that would otherwise be wildcards are escaped first, so "100%" searches for
// the text "100%" rather than everything.
func containsPattern(value string) string {
	escaped := strings.NewReplacer(likeEscape, likeEscape+likeEscape, "%", likeEscape+"%", "_", likeEscape+"_").Replace(value)
	return "%" + escaped + "%"
}

func comparison(op string) string {
	switch op {
	case Ne:
		return "<>"
	case GT:
		return ">"
	case GTE:
		return ">="
	case LT:
		return "<"
	case LTE:
		return "<="
	}
	return "="
}

// valueLiteral renders the right-hand side. A numeric column gets the number
// (and an empty value becomes NULL, which the caller turns into IS NULL where
// that is meaningful); a text column gets an escaped literal, so an empty text
// value searches for the empty string rather than NULL.
func valueLiteral(dbType, colType, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	numeric := sqltext.NewColumnType(colType).IsNumeric()
	if numeric {
		if trimmed == "" {
			return "NULL", nil
		}
		if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
			return "", fmt.Errorf("%q 不是数字", value)
		}
		return trimmed, nil
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("a value cannot contain NUL")
	}
	return sqltext.QuotedLiteral(dbType, value), nil
}
