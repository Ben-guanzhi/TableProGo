package main

import (
	"database/sql"
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
