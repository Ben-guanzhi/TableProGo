package filters

import (
	"strings"
	"testing"

	"github.com/dbcore/dbcore/sqltext"

	"tablepro-go/internal/catalogmodel"
)

func condition(t *testing.T, dbType, colType string, filter Condition) string {
	t.Helper()
	quoted, err := sqltext.QuoteIdent(dbType, filter.Column)
	if err != nil {
		t.Fatalf("quote %s: %v", filter.Column, err)
	}
	text, err := render(dbType, colType, quoted, filter)
	if err != nil {
		t.Fatalf("condition failed: %v", err)
	}
	return text
}

func TestFilterConditionsByDialect(t *testing.T) {
	cases := []struct {
		name    string
		dbType  string
		colType string
		filter  Condition
		want    string
	}{
		{
			name:    "text equality keeps the quotes",
			dbType:  "sqlite",
			colType: "text",
			filter:  Condition{Column: "name", Op: Eq, Value: "ab"},
			want:    `"name" = 'ab'`,
		},
		{
			name:    "sql server needs the national prefix",
			dbType:  "sqlserver",
			colType: "nvarchar",
			filter:  Condition{Column: "name", Op: Ne, Value: "it's"},
			want:    `[name] <> N'it''s'`,
		},
		{
			name:    "a number stays a number",
			dbType:  "postgresql",
			colType: "integer",
			filter:  Condition{Column: "qty", Op: GTE, Value: " 12 "},
			want:    `"qty" >= 12`,
		},
		{
			name:    "an absent value is compared with IS",
			dbType:  "mysql",
			colType: "int",
			filter:  Condition{Column: "qty", Op: Eq, Value: ""},
			want:    "`qty` IS NULL",
		},
		{
			name:    "and its negation with IS NOT",
			dbType:  "sqlite",
			colType: "integer",
			filter:  Condition{Column: "qty", Op: NotNull},
			want:    `"qty" IS NOT NULL`,
		},
		{
			name:    "an empty text column means the empty string",
			dbType:  "sqlite",
			colType: "text",
			filter:  Condition{Column: "name", Op: Eq, Value: ""},
			want:    `"name" = ''`,
		},
		{
			name:    "contains wraps an escaped pattern",
			dbType:  "sqlite",
			colType: "text",
			filter:  Condition{Column: "note", Op: Contains, Value: "100%_a/"},
			want:    `"note" LIKE '%100/%/_a//%' ESCAPE '/'`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := condition(t, tc.dbType, tc.colType, tc.filter); got != tc.want {
				t.Errorf("want %s\ngot  %s", tc.want, got)
			}
		})
	}
}

// A filter that matches nothing is worse than one that fails: the page looks
// empty as if the data were gone. So a one-sided comparison with no value is
// refused rather than turned into "col > NULL".
func TestFilterConditionsThatCannotMatchAreRefused(t *testing.T) {
	cases := []struct {
		name    string
		colType string
		filter  Condition
		want    string
	}{
		{
			name:    "greater than with no value",
			colType: "integer",
			filter:  Condition{Column: "qty", Op: GT, Value: ""},
			want:    "需要一个值",
		},
		{
			name:    "contains on a number",
			colType: "bigint",
			filter:  Condition{Column: "qty", Op: Contains, Value: "12"},
			want:    "不适用",
		},
		{
			name:    "text that is not a number",
			colType: "integer",
			filter:  Condition{Column: "qty", Op: Eq, Value: "twelve"},
			want:    "不是数字",
		},
		{
			name:    "an operator the bar never sends",
			colType: "text",
			filter:  Condition{Column: "qty", Op: "DROP", Value: "1"},
			want:    "unknown filter operator",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quoted, err := sqltext.QuoteIdent("sqlite", tc.filter.Column)
			if err != nil {
				t.Fatalf("quote: %v", err)
			}
			_, err = render("sqlite", tc.colType, quoted, tc.filter)
			if err == nil {
				t.Fatalf("want an error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// The bar names columns, it cannot name SQL: a column that is not part of the
// table is refused before a statement is built.
func TestWhereRefusesColumnsThatAreNotThere(t *testing.T) {
	columns := []catalogmodel.ColumnInfo{{Name: "name"}, {Name: "id"}}
	_, err := Where("sqlite", "parts", columns, []Condition{
		{Column: "name; DROP TABLE parts", Op: Eq, Value: "x"},
	})
	if err == nil {
		t.Fatal("want an error for a column that is not in the table")
	}
	if !strings.Contains(err.Error(), "is not a column of parts") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Conditions are matched case-insensitively against catalog names and ANDed.
func TestWhereAndsConditionsByCatalogName(t *testing.T) {
	columns := []catalogmodel.ColumnInfo{
		{Name: "name", Type: "text"},
		{Name: "id", Type: "integer"},
	}
	where, err := Where("sqlite", "parts", columns, []Condition{
		{Column: "NAME", Op: Contains, Value: "gear"},
		{Column: "id", Op: LT, Value: "5"},
	})
	if err != nil {
		t.Fatalf("where failed: %v", err)
	}
	if want := ` WHERE "name" LIKE '%gear%' ESCAPE '/' AND "id" < 5`; where != want {
		t.Errorf("want %s\ngot  %s", want, where)
	}
	if empty, err := Where("sqlite", "parts", columns, nil); err != nil || empty != "" {
		t.Errorf("want no WHERE, got %q (err %v)", empty, err)
	}
}

// The numeric filter value goes through the same gate as a saved edit: an
// exact decimal shape (a big-int key included) keeps its digits, and the
// float path is restricted to shapes every engine accepts as a literal.
// strconv.ParseFloat alone would let "NaN", "Inf", "0x1p2" and "1_000"
// through — the raw text of each is a syntax error on at least one engine.
func TestFilterNumericValueValidation(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string // rendered literal, or "" when the value must be refused
	}{
		{name: "big-int key passes unrounded", value: "9007199254740993", want: "9007199254740993"},
		{name: "decimal with trailing zeros keeps them", value: "1.50", want: "1.50"},
		{name: "scientific notation stays on the float path", value: "1e3", want: "1e3"},
		{name: "leading dot is a float", value: ".5", want: ".5"},
		{name: "NaN is not a number literal", value: "NaN", want: ""},
		{name: "Inf is not a number literal", value: "Inf", want: ""},
		{name: "negative infinity is refused", value: "-Infinity", want: ""},
		{name: "hex float is refused", value: "0x1p2", want: ""},
		{name: "digit separator is refused", value: "1_000", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quoted := `"id"`
			got, err := render("mysql", "integer", quoted, Condition{Column: "id", Op: Eq, Value: tc.value})
			if tc.want == "" {
				if err == nil {
					t.Fatalf("want the value refused, got %s", got)
				}
				if !strings.Contains(err.Error(), "不是数字") {
					t.Errorf("error %q does not say the value is not a number", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("want %s, got error: %v", tc.want, err)
			}
			if want := quoted + " = " + tc.want; got != want {
				t.Errorf("want %s\ngot  %s", want, got)
			}
		})
	}
}
