package main

import (
	"testing"
)

func TestSplitSQLScriptDelegatesToPackage(t *testing.T) {
	// The scanner itself is covered by internal/sqlscript's own tests; here we
	// only pin that the package-level helper (used by the script import) is
	// wired to it.
	got := SplitSQLScript("SELECT 1;SELECT ';';")
	if len(got) != 2 || got[1] != "SELECT ';'" {
		t.Fatalf("delegation = %q", got)
	}
}
func TestImportSQLScriptRunsInOneTransaction(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeTempFile(t, "script.sql",
		"CREATE TABLE s(id INTEGER PRIMARY KEY, name TEXT);\nINSERT INTO s VALUES (1,'a');\nINSERT INTO s VALUES (2,'b');\n")

	res, err := app.ImportSQLScript(ImportRequest{ConnID: "c1", Path: path})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Statements != 3 {
		t.Fatalf("statements = %d", res.Statements)
	}
	if r := app.ExecuteQuery("c1", "SELECT id FROM s ORDER BY id", ""); r.Error != "" || r.RowCount != 2 {
		t.Fatalf("rows = %+v", r)
	}
}

func TestImportSQLScriptRollsBackOnError(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeTempFile(t, "bad.sql",
		"CREATE TABLE bad(id INTEGER PRIMARY KEY);INSERT INTO bad VALUES (1);INSERT INTO nope VALUES (2);\n")

	if _, err := app.ImportSQLScript(ImportRequest{ConnID: "c1", Path: path}); err == nil {
		t.Fatal("expected the script to fail")
	}
	// The CREATE went into the transaction too, so the table must be gone.
	r := app.ExecuteQuery("c1", "SELECT name FROM sqlite_master WHERE name = 'bad'", "")
	if r.Error != "" || r.RowCount != 0 {
		t.Fatalf("partial script survived: %+v", r)
	}
}

func TestImportSQLScriptSkipErrors(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeTempFile(t, "skip.sql",
		"CREATE TABLE sk(id INTEGER PRIMARY KEY);INSERT INTO sk VALUES (1);INSERT INTO nope VALUES (2);INSERT INTO sk VALUES (3);\n")

	res, err := app.ImportSQLScript(ImportRequest{ConnID: "c1", Path: path, Options: ImportOptions{SkipErrors: true}})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Statements != 3 || res.Skipped != 1 {
		t.Fatalf("result = %+v", res)
	}
	if r := app.ExecuteQuery("c1", "SELECT id FROM sk ORDER BY id", ""); r.RowCount != 2 {
		t.Fatalf("rows = %+v", r)
	}
}

func TestImportFileRoutesSQLByExtension(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeTempFile(t, "dump.sql", "CREATE TABLE d(id INTEGER);INSERT INTO d VALUES (7);")
	res, err := app.ImportFile(ImportRequest{ConnID: "c1", Path: path})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Statements != 2 {
		t.Fatalf("result = %+v", res)
	}
	if r := app.ExecuteQuery("c1", "SELECT id FROM d", ""); r.RowCount != 1 {
		t.Fatalf("rows = %+v", r)
	}
}
