package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestImportCSVCreatesTableWithInferredTypes(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeTempFile(t, "people.csv", "id,name,score\n1,alice,9.5\n2,bob,7\n")

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "people", Path: path,
		Options: ImportOptions{HasHeader: true, CreateTable: true, InferTypes: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !res.CreatedTable || res.Rows != 2 || res.Statements != 1 || res.Skipped != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Columns) != 3 || res.Columns[1] != "name" {
		t.Fatalf("columns = %v", res.Columns)
	}

	// Inferred types: id INTEGER, name TEXT, score REAL.
	cols, err := app.GetColumns("c1", "", "", "people")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	types := map[string]string{}
	for _, c := range cols {
		types[c.Name] = c.Type
	}
	for name, want := range map[string]string{"id": "INTEGER", "name": "TEXT", "score": "REAL"} {
		if got := types[name]; got != want {
			t.Errorf("column %s type = %q, want %q", name, got, want)
		}
	}

	r := app.ExecuteQuery("c1", "SELECT id,name FROM people ORDER BY id", "")
	if r.Error != "" || r.RowCount != 2 {
		t.Fatalf("rows = %+v", r)
	}
}

func TestImportJSONWithNulls(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeTempFile(t, "events.json", `[{"id":1,"note":"a"},{"id":2,"note":null}]`)

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "events", Path: path,
		Options: ImportOptions{CreateTable: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Rows != 2 {
		t.Fatalf("result = %+v", res)
	}
	r := app.ExecuteQuery("c1", "SELECT note FROM events WHERE id = 2", "")
	if r.Error != "" || r.RowCount != 1 || r.Rows[0][0] != nil {
		t.Fatalf("null round-trip = %+v", r)
	}
}

func TestImportTruncateReplacesRows(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE t2(id INTEGER, name TEXT)", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	if r := app.ExecuteQuery("c1", "INSERT INTO t2 VALUES (99,'old')", ""); r.Error != "" {
		t.Fatalf("seed: %s", r.Error)
	}
	path := writeTempFile(t, "t2.csv", "id,name\n1,new\n")

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "t2", Path: path,
		Options: ImportOptions{HasHeader: true, Mode: "truncate"},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Rows != 1 || res.CreatedTable {
		t.Fatalf("result = %+v", res)
	}
	r := app.ExecuteQuery("c1", "SELECT name FROM t2", "")
	if r.RowCount != 1 || r.Rows[0][0] != "new" {
		t.Fatalf("truncate did not replace rows: %+v", r)
	}
}

func TestImportSkipErrorsCountsBadRows(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	// A unique key makes the duplicate row fail while the others succeed.
	if r := app.ExecuteQuery("c1", "CREATE TABLE u(id INTEGER PRIMARY KEY, name TEXT)", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	path := writeTempFile(t, "u.csv", "id,name\n1,a\n1,dup\n2,b\n")

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "u", Path: path,
		Options: ImportOptions{HasHeader: true, SkipErrors: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Rows != 2 || res.Skipped < 1 {
		t.Fatalf("result = %+v", res)
	}
	r := app.ExecuteQuery("c1", "SELECT id FROM u ORDER BY id", "")
	if r.RowCount != 2 {
		t.Fatalf("rows in table = %d, want 2", r.RowCount)
	}
}

func TestImportRollsBackWholeBatchOnError(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE r(id INTEGER PRIMARY KEY, name TEXT)", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	path := writeTempFile(t, "r.csv", "id,name\n1,a\n1,dup\n2,b\n")

	if _, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "r", Path: path,
		Options: ImportOptions{HasHeader: true},
	}); err == nil {
		t.Fatal("expected the batch to fail without SkipErrors")
	}
	// The transaction rolled back: the first row must not be there either.
	r := app.ExecuteQuery("c1", "SELECT id FROM r", "")
	if r.Error != "" || r.RowCount != 0 {
		t.Fatalf("partial import survived: %+v", r)
	}
}

func TestImportCSVWithoutHeaderGetsGeneratedNames(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeTempFile(t, "noheader.csv", "1,a\n2,b\n")
	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "nh", Path: path,
		Options: ImportOptions{HasHeader: false, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Columns) != 2 || res.Columns[0] != "column1" || res.Columns[1] != "column2" {
		t.Fatalf("columns = %v", res.Columns)
	}
	if res.Rows != 2 {
		t.Fatalf("rows = %d", res.Rows)
	}
}

// TestImportMapsColumnsIntoExistingTable pins the column mapping: a file whose
// headers differ in case and order, and that carries a column the table does
// not have, still lands on the right columns instead of failing the INSERT —
// and the pairing is reported, not guessed silently.
func TestImportMapsColumnsIntoExistingTable(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)

	// The table exists with lowercase columns the file spells differently.
	if r := app.ExecuteQuery("c1", "CREATE TABLE map_people (id INTEGER PRIMARY KEY, name TEXT, note TEXT)", ""); r.Error != "" {
		t.Fatalf("create table: %s", r.Error)
	}

	path := writeTempFile(t, "people2.csv", "Name,ID,extra\ncarol,3,z\n")
	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "map_people", Path: path,
		Options: ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("mapped import: %v", err)
	}
	if res.CreatedTable {
		t.Fatal("the table already existed; the second run must land on it")
	}
	if res.Rows != 1 || res.Skipped != 0 {
		t.Fatalf("result = %+v, want 1 row written", res)
	}
	if len(res.Mapped) != 3 {
		t.Fatalf("mapped = %+v, want one pair per file column", res.Mapped)
	}
	byFile := map[string]string{}
	for _, m := range res.Mapped {
		byFile[m.File] = m.Table
	}
	if byFile["Name"] != "name" || byFile["ID"] != "id" {
		t.Fatalf("mapping = %v, want Name->name and ID->id", byFile)
	}
	if byFile["extra"] != "" {
		t.Fatalf("extra should be reported unmatched, got %q", byFile["extra"])
	}

	r := app.ExecuteQuery("c1", "SELECT id, name FROM map_people ORDER BY id", "")
	if r.Error != "" || r.RowCount != 1 {
		t.Fatalf("rows = %+v", r)
	}
	// The values landed in the right columns: carol is a name, 3 an id —
	// despite the file spelling them Name/ID and carrying an extra column.
	if got := fmt.Sprintf("%v|%v", r.Rows[0][0], r.Rows[0][1]); got != "3|carol" {
		t.Fatalf("row 1 = %s, want 3|carol", got)
	}
}

// TestImportHeaderlessIntoExistingTableMapsByPosition covers the only identity
// a headerless file has: column order.
func TestImportHeaderlessIntoExistingTableMapsByPosition(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeTempFile(t, "pairs.csv", "id,name,score\n1,alice,9.5\n2,bob,7\n")
	if _, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "plain_people", Path: path,
		Options: ImportOptions{HasHeader: true, CreateTable: true, InferTypes: true},
	}); err != nil {
		t.Fatalf("setup import: %v", err)
	}

	path = writeTempFile(t, "pairs2.csv", "3,carol,8.1\n")
	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "plain_people", Path: path,
		Options: ImportOptions{HasHeader: false, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("headerless import: %v", err)
	}
	if res.Rows != 1 {
		t.Fatalf("result = %+v, want 1 row", res)
	}
	r := app.ExecuteQuery("c1", "SELECT id, name FROM plain_people WHERE id = 3", "")
	if r.Error != "" || r.RowCount != 1 || fmt.Sprintf("%v", r.Rows[0][1]) != "carol" {
		t.Fatalf("rows = %+v, want 3|carol", r)
	}
}
