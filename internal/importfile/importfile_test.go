package importfile

import (
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
)

func TestParseCSVWithHeader(t *testing.T) {
	cols, rows, err := ParseCSV(strings.NewReader("id,name\n1,alice\n2,bob\n"), Options{HasHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[1] != "name" || len(rows) != 2 {
		t.Fatalf("cols=%v rows=%d", cols, len(rows))
	}
	if rows[0][1] == nil || *rows[0][1] != "alice" {
		t.Fatalf("first row = %v", rows[0])
	}
}

func TestParseCSVRaggedAndNoHeader(t *testing.T) {
	cols, rows, err := ParseCSV(strings.NewReader("1,a\n2\n"), Options{HasHeader: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0] != "column1" {
		t.Fatalf("cols = %v", cols)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	// A short record leaves the missing cell as NULL rather than erroring.
	if rows[1][1] != nil {
		t.Fatalf("ragged cell = %q, want nil", *rows[1][1])
	}
}

func TestParseJSONUnionOfKeys(t *testing.T) {
	cols, rows, err := ParseJSON(strings.NewReader(`[{"id":1,"name":"a"},{"id":2,"extra":true}]`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"extra", "id", "name"}; strings.Join(cols, ",") != strings.Join(want, ",") {
		t.Fatalf("cols = %v, want %v", cols, want)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0][0] != nil { // "extra" missing in the first object -> NULL
		t.Fatalf("missing key should be NULL, got %q", *rows[0][0])
	}
	if rows[1][0] == nil || *rows[1][0] != "true" {
		t.Fatalf("bool cell = %v", rows[1][0])
	}
}

func TestParseJSONSingleObject(t *testing.T) {
	cols, rows, err := ParseJSON(strings.NewReader(`{"id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || len(rows) != 1 || *rows[0][0] != "7" {
		t.Fatalf("cols=%v rows=%v", cols, rows)
	}
}

func TestInferTypes(t *testing.T) {
	rows := [][]*string{
		{strPtr("1"), strPtr("1.5"), strPtr("x"), nil},
		{strPtr("2"), strPtr("2"), strPtr("y"), nil},
	}
	kinds := InferTypes([]string{"id", "score", "label", "empty"}, rows)
	want := []string{"int", "real", "text", "text"}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("%s = %q, want %q", []string{"id", "score", "label", "empty"}[i], kinds[i], want[i])
		}
	}
	// A later non-numeric value downgrades the column.
	kinds = InferTypes([]string{"v"}, [][]*string{{strPtr("1")}, {strPtr("abc")}})
	if kinds[0] != "text" {
		t.Fatalf("downgrade = %q", kinds[0])
	}
}

func TestColumnTypePerEngine(t *testing.T) {
	cases := []struct{ engine, kind, want string }{
		{"sqlite", "int", "INTEGER"},
		{"postgresql", "int", "BIGINT"},
		{"mysql", "real", "DOUBLE"},
		{"postgresql", "real", "DOUBLE PRECISION"},
		{"sqlserver", "text", "NVARCHAR(MAX)"},
		{"oracle", "text", "CLOB"},
		{"oracle", "int", "NUMBER(19)"},
	}
	for _, c := range cases {
		if got := ColumnType(c.engine, c.kind); got != c.want {
			t.Errorf("%s/%s = %q, want %q", c.engine, c.kind, got, c.want)
		}
	}
}

func TestCreateTableSQLQuotesPerEngine(t *testing.T) {
	if got := CreateTableSQL("postgresql", "t1", []string{"id"}, []string{"int"}); !strings.Contains(got, `"t1"`) {
		t.Errorf("pg DDL = %q", got)
	}
	if got := CreateTableSQL("mysql", "t1", []string{"id"}, []string{"int"}); !strings.Contains(got, "`t1`") {
		t.Errorf("mysql DDL = %q", got)
	}
	// Oracle leaves simple names unquoted so they fold to upper case.
	got := CreateTableSQL("oracle", "t1", []string{"id"}, []string{"int"})
	if !strings.Contains(got, "CREATE TABLE t1") || strings.Contains(got, `"t1"`) {
		t.Errorf("oracle DDL = %q", got)
	}
}

func TestInsertSQLBindsEveryCell(t *testing.T) {
	rows := [][]*string{{strPtr("1"), nil}, {strPtr("2"), strPtr("b")}}
	stmt, args, err := InsertSQL("postgresql", "t", []string{"id", "name"}, rows, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stmt, "$1") || !strings.Contains(stmt, "$4") {
		t.Fatalf("placeholders = %q", stmt)
	}
	if len(args) != 4 {
		t.Fatalf("args = %d", len(args))
	}
	if args[1].Kind != dbcore.ValNull {
		t.Fatalf("nil cell should bind as NULL, got %+v", args[1])
	}
	if args[3].S != "b" {
		t.Fatalf("second row cell = %+v", args[3])
	}
}

func TestInsertSQLUpsertClauses(t *testing.T) {
	pg, _, err := InsertSQL("postgresql", "t", []string{"id", "name"}, [][]*string{{strPtr("1"), strPtr("a")}},
		Options{Mode: "upsert", UpsertKeys: []string{"id"}})
	if err != nil || !strings.Contains(pg, `ON CONFLICT ("id") DO UPDATE SET "id"=excluded."id"`) {
		t.Fatalf("pg upsert = %q (%v)", pg, err)
	}
	my, _, err := InsertSQL("mysql", "t", []string{"id"}, [][]*string{{strPtr("1")}},
		Options{Mode: "upsert", UpsertKeys: []string{"id"}})
	if err != nil || !strings.Contains(my, "ON DUPLICATE KEY UPDATE") {
		t.Fatalf("mysql upsert = %q (%v)", my, err)
	}
	if _, _, err := InsertSQL("sqlserver", "t", []string{"id"}, [][]*string{{strPtr("1")}},
		Options{Mode: "upsert", UpsertKeys: []string{"id"}}); err == nil {
		t.Fatal("sqlserver upsert must be rejected")
	}
}

func TestOracleRejectsMultiRowInsert(t *testing.T) {
	if _, _, err := InsertSQL("oracle", "t", []string{"id"}, [][]*string{{strPtr("1")}, {strPtr("2")}}, Options{}); err == nil {
		t.Fatal("oracle multi-row insert must be rejected")
	}
}

func TestNormalizeInfersFormatAndMode(t *testing.T) {
	if got := Normalize(Options{}, "x.SQL"); got.Format != "sql" {
		t.Errorf("format = %q", got.Format)
	}
	if got := Normalize(Options{}, "x.json"); got.Format != "json" {
		t.Errorf("format = %q", got.Format)
	}
	if got := Normalize(Options{Mode: "bogus"}, "x.csv"); got.Mode != "append" {
		t.Errorf("mode = %q", got.Mode)
	}
	if got := Normalize(Options{Delimiter: ";;"}, "x.csv"); got.Delimiter != "," {
		t.Errorf("delimiter = %q", got.Delimiter)
	}
}

func strPtr(s string) *string { return &s }
