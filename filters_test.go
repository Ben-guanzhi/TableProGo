package main

import (
	"strings"
	"testing"
)

func condition(t *testing.T, dbType, colType string, filter TableFilter) string {
	t.Helper()
	quoted, err := quoteIdent(dbType, filter.Column)
	if err != nil {
		t.Fatalf("quote %s: %v", filter.Column, err)
	}
	text, err := filterCondition(dbType, colType, quoted, filter)
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
		filter  TableFilter
		want    string
	}{
		{
			name:    "text equality keeps the quotes",
			dbType:  "sqlite",
			colType: "text",
			filter:  TableFilter{Column: "name", Op: filterEq, Value: "ab"},
			want:    `"name" = 'ab'`,
		},
		{
			name:    "sql server needs the national prefix",
			dbType:  "sqlserver",
			colType: "nvarchar",
			filter:  TableFilter{Column: "name", Op: filterNe, Value: "it's"},
			want:    `[name] <> N'it''s'`,
		},
		{
			name:    "a number stays a number",
			dbType:  "postgresql",
			colType: "integer",
			filter:  TableFilter{Column: "qty", Op: filterGTE, Value: " 12 "},
			want:    `"qty" >= 12`,
		},
		{
			name:    "an absent value is compared with IS",
			dbType:  "mysql",
			colType: "int",
			filter:  TableFilter{Column: "qty", Op: filterEq, Value: ""},
			want:    "`qty` IS NULL",
		},
		{
			name:    "and its negation with IS NOT",
			dbType:  "sqlite",
			colType: "integer",
			filter:  TableFilter{Column: "qty", Op: filterNotNull},
			want:    `"qty" IS NOT NULL`,
		},
		{
			name:    "an empty text column means the empty string",
			dbType:  "sqlite",
			colType: "text",
			filter:  TableFilter{Column: "name", Op: filterEq, Value: ""},
			want:    `"name" = ''`,
		},
		{
			name:    "contains wraps an escaped pattern",
			dbType:  "sqlite",
			colType: "text",
			filter:  TableFilter{Column: "note", Op: filterContains, Value: "100%_a/"},
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
		filter  TableFilter
		want    string
	}{
		{
			name:    "greater than with no value",
			colType: "integer",
			filter:  TableFilter{Column: "qty", Op: filterGT, Value: ""},
			want:    "需要一个值",
		},
		{
			name:    "contains on a number",
			colType: "bigint",
			filter:  TableFilter{Column: "qty", Op: filterContains, Value: "12"},
			want:    "不适用",
		},
		{
			name:    "text that is not a number",
			colType: "integer",
			filter:  TableFilter{Column: "qty", Op: filterEq, Value: "twelve"},
			want:    "not a valid",
		},
		{
			name:    "an operator the bar never sends",
			colType: "text",
			filter:  TableFilter{Column: "qty", Op: "DROP", Value: "1"},
			want:    "unknown filter operator",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quoted, err := quoteIdent("sqlite", tc.filter.Column)
			if err != nil {
				t.Fatalf("quote: %v", err)
			}
			_, err = filterCondition("sqlite", tc.colType, quoted, tc.filter)
			if err == nil {
				t.Fatalf("want an error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestTableWhereAndsConditionsByCatalogName(t *testing.T) {
	conn := sqliteDB(t, "conn_w", "scratch")
	createPartsTable(t, conn)

	where, err := searchApp(conn).tableWhere(conn.ID, conn.Database, "", "parts", []TableFilter{
		{Column: "NAME", Op: filterContains, Value: "gear"},
		{Column: "id", Op: filterLT, Value: "5"},
	})
	if err != nil {
		t.Fatalf("where failed: %v", err)
	}
	if want := ` WHERE "name" LIKE '%gear%' ESCAPE '/' AND "id" < 5`; where != want {
		t.Errorf("want %s\ngot  %s", want, where)
	}

	// No conditions means the query is left exactly as paging built it.
	if empty, err := searchApp(conn).tableWhere(conn.ID, conn.Database, "", "parts", nil); err != nil || empty != "" {
		t.Errorf("want no WHERE, got %q (err %v)", empty, err)
	}
}

// The bar names columns, it cannot name SQL: anything that is not a column of
// the table is refused before a statement is built.
func TestTableWhereRefusesColumnsThatAreNotThere(t *testing.T) {
	conn := sqliteDB(t, "conn_w2", "scratch")
	createPartsTable(t, conn)

	_, err := searchApp(conn).tableWhere(conn.ID, conn.Database, "", "parts", []TableFilter{
		{Column: "name; DROP TABLE parts", Op: filterEq, Value: "x"},
	})
	if err == nil {
		t.Fatalf("want an error for a column that is not in the table")
	}
	if !strings.Contains(err.Error(), "is not a column of parts") {
		t.Errorf("unexpected error: %v", err)
	}
}

// The whole point of the bar is a shorter page: the WHERE has to land in the
// query the data page runs, and the page window still applies on top of it.
func TestPreviewAppliesFiltersBeforePaging(t *testing.T) {
	conn := sqliteDB(t, "conn_filter", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE widgets (id integer primary key, label text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, label := range []string{"bolt", "bolt nut", "gear", "gear box", "gear oil"} {
		if _, err := conn.DB.Exec(`INSERT INTO widgets (label) VALUES (?)`, label); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	a := searchApp(conn)
	preview := func(limit, offset int, filters ...TableFilter) []string {
		t.Helper()
		result := a.Preview(conn.ID, conn.Database, "", "widgets", limit, offset, filters)
		if result.Error != "" {
			t.Fatalf("preview failed: %s", result.Error)
		}
		var labels []string
		for _, row := range result.Rows {
			labels = append(labels, row[len(row)-1].(string))
		}
		return labels
	}

	got := preview(10, 0, TableFilter{Column: "label", Op: filterContains, Value: "gear"})
	if strings.Join(got, ",") != "gear,gear box,gear oil" {
		t.Errorf("contains matched %v", got)
	}
	// The wildcards are escaped, so a query for "gear b" cannot match "gear-oil-b".
	got = preview(10, 0, TableFilter{Column: "label", Op: filterEq, Value: "bolt"})
	if strings.Join(got, ",") != "bolt" {
		t.Errorf("equality matched %v", got)
	}
	got = preview(2, 2, TableFilter{Column: "label", Op: filterContains, Value: "gear"})
	if strings.Join(got, ",") != "gear oil" {
		t.Errorf("page 3 of the filtered rows: %v", got)
	}
	got = preview(10, 0, TableFilter{Column: "id", Op: filterGT, Value: "3"})
	if strings.Join(got, ",") != "gear box,gear oil" {
		t.Errorf("comparison matched %v", got)
	}
	if none := preview(10, 0); len(none) != 5 {
		t.Errorf("no filters should keep every row, got %v", none)
	}
}

// A bad condition surfaces as the data card's error rather than a silent empty
// page, and the rows stay where they are.
func TestPreviewReportsAFilterItCannotBuild(t *testing.T) {
	conn := sqliteDB(t, "conn_filter_bad", "scratch")
	createPartsTable(t, conn)

	result := searchApp(conn).Preview(conn.ID, conn.Database, "", "parts", 10, 0,
		[]TableFilter{{Column: "id", Op: filterEq, Value: "twelve"}})
	if result.Error == "" || !strings.Contains(result.Error, "not a valid") {
		t.Fatalf("want a type error, got %+v", result)
	}
}
