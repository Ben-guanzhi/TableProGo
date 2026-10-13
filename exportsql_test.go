package main

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// The export has to render a value the way a save would, or the script rebuilds
// the table as something other than what the grid shows.
func TestInsertScriptRendersLiteralsByColumnType(t *testing.T) {
	types := map[string]string{"id": "integer", "name": "varchar(50)", "note": "text"}
	rows := [][]interface{}{
		{float64(1), "O'Brien", nil},
		// A number that arrived as text still has to go out unquoted, and an
		// empty numeric cell is NULL rather than 0.
		{"2", "", "多行\n文本"},
	}

	got, err := insertScript("sqlite", `"parts"`, "parts", []string{"id", "name", "note"}, types, rows)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	want := `INSERT INTO "parts" ("id", "name", "note") VALUES (1, 'O''Brien', NULL);
INSERT INTO "parts" ("id", "name", "note") VALUES (2, '', '多行
文本');`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	// SQL Server needs the national prefix or the wide characters are lost.
	sqlserver, err := insertScript("sqlserver", "[dbo].[parts]", "parts", []string{"id", "name"}, types,
		[][]interface{}{{float64(7), "阿"}})
	if err != nil {
		t.Fatalf("sqlserver script: %v", err)
	}
	if sqlserver != "INSERT INTO [dbo].[parts] ([id], [name]) VALUES (7, N'阿');" {
		t.Errorf("got: %s", sqlserver)
	}
}

// A result that picked fields out of a table can still be shown; only rows that
// came out of the table itself can be written back as INSERT.
func TestInsertScriptRefusesColumnsThatAreNotFields(t *testing.T) {
	types := map[string]string{"id": "integer", "name": "text"}
	one := [][]interface{}{{float64(1), "a"}}

	if _, err := insertScript("sqlite", `"parts"`, "parts", []string{"id", "upper(name)"}, types, one); err == nil ||
		!strings.Contains(err.Error(), "不是表 parts 的字段") {
		t.Errorf("want a refusal for a computed column, got: %v", err)
	}
	if _, err := insertScript("sqlite", `"parts"`, "parts", []string{"id", "id"}, types,
		[][]interface{}{{float64(1), float64(1)}}); err == nil ||
		!strings.Contains(err.Error(), "同名列") {
		t.Errorf("want a refusal for a repeated column, got: %v", err)
	}
	if _, err := insertScript("sqlite", `"parts"`, "parts", []string{"id", "name"}, types,
		[][]interface{}{{float64(1)}}); err == nil ||
		!strings.Contains(err.Error(), "对不上") {
		t.Errorf("want a refusal for a short row, got: %v", err)
	}
}

// A value the column cannot hold has to name the row it came from, or the user
// hunts through a hundred statements for the one bad cell.
func TestInsertScriptPointsAtTheRowThatFailed(t *testing.T) {
	types := map[string]string{"id": "integer", "name": "text"}
	_, err := insertScript("sqlite", `"parts"`, "parts", []string{"id", "name"}, types,
		[][]interface{}{{float64(1), "a"}, {"not a number", "b"}})
	if err == nil || !strings.Contains(err.Error(), "第 2 行的 id") {
		t.Fatalf("want the failure pinned to row 2, got: %v", err)
	}
}

// The whole path, catalog included: the script a table produces has to reload
// that table somewhere else and give the same rows back.
func TestInsertScriptRebuildsTheRowsItRead(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	ddl := `CREATE TABLE "odd name" ("a b" integer primary key, note text, score real)`
	if _, err := conn.DB.Exec(ddl); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO "odd name" VALUES (1, 'It''s here', 1.5)`,
		`INSERT INTO "odd name" VALUES (2, NULL, 0)`,
	} {
		if _, err := conn.DB.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	rows := [][]interface{}{
		{float64(1), "It's here", float64(1.5)},
		{float64(2), nil, float64(0)},
	}

	script, err := searchApp(conn).InsertScript(conn.ID, "", "", "odd name",
		[]string{"a b", "note", "score"}, rows)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	if !strings.HasPrefix(script, `INSERT INTO "odd name" ("a b", "note", "score") VALUES (1, 'It''s here', 1.5);`) {
		t.Fatalf("unexpected script: %s", script)
	}

	// Replay it into an empty copy of the table, semicolons and all.
	replay := sqliteDB(t, "conn_1", "replay")
	if _, err := replay.DB.Exec(ddl); err != nil {
		t.Fatalf("create replay table: %v", err)
	}
	for _, statement := range strings.Split(script, "\n") {
		if _, err := replay.DB.Exec(statement); err != nil {
			t.Fatalf("replay %q: %v", statement, err)
		}
	}
	var count int
	if err := replay.DB.QueryRow(`SELECT COUNT(*) FROM "odd name"`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("want 2 rows after replay, got %d", count)
	}
	var note sql.NullString
	if err := replay.DB.QueryRow(`SELECT note FROM "odd name" WHERE "a b" = 1`).Scan(&note); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if note.String != "It's here" {
		t.Errorf("note = %q, want %q", note.String, "It's here")
	}

	// A result with no table behind it is the case the menu warns about.
	if _, err := searchApp(conn).InsertScript(conn.ID, "", "", "", []string{"a b"}, rows); err == nil ||
		!strings.Contains(err.Error(), "没有来源表") {
		t.Errorf("want a refusal without a table, got: %v", err)
	}
	if _, err := searchApp(conn).InsertScript(conn.ID, "", "", "odd name", []string{"a b"}, nil); err == nil ||
		!strings.Contains(err.Error(), "没有可导出的行") {
		t.Errorf("want a refusal for an empty result, got: %v", err)
	}
}

func TestInsertScriptRefusesMoreRowsThanOneExportHolds(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE parts (id integer primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	oversized := make([][]interface{}, insertExportMaxRows+1)
	for i := range oversized {
		oversized[i] = []interface{}{i}
	}

	if _, err := searchApp(conn).InsertScript(conn.ID, "", "", "parts", []string{"id"}, oversized); err == nil ||
		!strings.Contains(err.Error(), "一次最多导出") {
		t.Fatalf("want a refusal above the cap, got: %v", err)
	}
}

// oldInsertScript is the pre-builder renderer kept verbatim as the oracle for
// the optimized one: join the column list per row, Sprintf one statement per
// row, Join the statements at the end. Byte-for-byte equality with this is the
// whole contract of the strings.Builder rewrite.
func oldInsertScript(
	dbType string, name string, table string,
	resultColumns []string, types map[string]string, rows [][]interface{},
) (string, error) {
	idents := make([]string, len(resultColumns))
	colTypes := make([]string, len(resultColumns))
	for i, col := range resultColumns {
		colType, ok := types[strings.ToLower(col)]
		if !ok {
			return "", fmt.Errorf("结果列 %s 不是表 %s 的字段", col, table)
		}
		ident, err := quoteIdent(dbType, col)
		if err != nil {
			return "", err
		}
		idents[i] = ident
		colTypes[i] = colType
	}
	statements := make([]string, 0, len(rows))
	for _, row := range rows {
		values := make([]string, len(row))
		for j, value := range row {
			literal, err := valueLiteral(dbType, colTypes[j], value)
			if err != nil {
				return "", err
			}
			values[j] = literal
		}
		statements = append(statements, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);",
			name, strings.Join(idents, ", "), strings.Join(values, ", ")))
	}
	return strings.Join(statements, "\n"), nil
}

// The builder must not change one byte of the script, in any dialect, for any
// of the shapes a grid can hold - including the empty result the App layer
// normally refuses earlier.
func TestInsertScriptBuilderMatchesTheOldRendererByteForByte(t *testing.T) {
	cols := []string{"id", "name", "note", "score", "city"}
	types := map[string]string{
		"id": "integer", "name": "varchar(50)", "note": "text",
		"score": "decimal(10,2)", "city": "nvarchar(30)",
	}
	rows := [][]interface{}{
		{float64(1), "O'Brien", nil, "12.50", "上海"},
		{"2", "", "多行\n文本", "0", nil},
		{float64(3), "back\\slash", "a'b'c", "-1.25", "x"},
	}
	for _, dbType := range []string{"sqlite", "mysql", "postgresql", "sqlserver", "oracle"} {
		got, err := insertScript(dbType, `"parts"`, "parts", cols, types, rows)
		want, wantErr := oldInsertScript(dbType, `"parts"`, "parts", cols, types, rows)
		if (err == nil) != (wantErr == nil) || got != want {
			t.Errorf("%s: got %q (%v), want %q (%v)", dbType, got, err, want, wantErr)
		}
		if out, err := insertScript(dbType, `"parts"`, "parts", cols, types, nil); err != nil || out != "" {
			t.Errorf("empty result: got %q with err %v, want empty and no error", out, err)
		}
	}
}

// The mechanism, not just the output: the old loop allocated the row's values
// slice, two Joins and one Sprintf per row - about a dozen allocations per
// row against the builder's literal-only six. If someone restores any per-row
// boilerplate this number moves and the test goes red.
func TestInsertScriptAllocatesOnlyTheLiteralsPerRow(t *testing.T) {
	const rowCount = 1000
	cols, types, rows := benchExportRows(rowCount)
	allocs := testing.AllocsPerRun(20, func() {
		if _, err := insertScript("sqlite", `"parts"`, "parts", cols, types, rows); err != nil {
			t.Fatal(err)
		}
	})
	// Five cells a row render with at most one string each - measured 5.7
	// allocations per row - plus growth headroom for the builder itself. The
	// old loop needed about 12.7 per row (values slice, two joins, one
	// Sprintf); a per-row join creeping back alone also crosses this line.
	if ceiling := float64(rowCount*6 + 64); allocs > ceiling {
		t.Errorf("insertScript allocated %v times for %d rows, ceiling %v: "+
			"the column list must be joined once per export, not once per row", allocs, rowCount, ceiling)
	}
}
