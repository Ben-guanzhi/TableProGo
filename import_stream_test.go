package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tablepro-go/internal/importfile"
)

// A file larger than one stream chunk imports in full through the chunked
// path, inside one transaction (a failure mid-file still rolls everything
// back).
func TestImportCSVStreamsBeyondOneChunk(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	total := importfile.StreamChunkSize + 250

	var sb strings.Builder
	sb.WriteString("id,name\n")
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&sb, "%d,name%d\n", i, i)
	}
	path := writeTempFile(t, "big.csv", sb.String())

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "big", Path: path,
		Options: ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("import: %v", res.Error)
	}
	if res.Rows != total || res.Statements < total/importBatchSize {
		t.Fatalf("result = %+v", res)
	}
	var count int
	if err := app.connections["c1"].DB.QueryRow(`SELECT COUNT(*) FROM big`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != total {
		t.Fatalf("rows in table = %d, want %d", count, total)
	}
}

// With skipErrors the first failing row is reported with its 1-based file row
// number, instead of a silent "skipped N". The failure is a real constraint:
// SQLite's type affinity would happily store "not-a-number" in an INTEGER
// column, so the test uses a duplicate primary key.
func TestImportSkipErrorsReportsFirstBadRow(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if _, err := app.connections["c1"].DB.Exec(`CREATE TABLE mixed (id integer primary key, name text)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	path := writeTempFile(t, "mixed.csv", "id,name\n1,ok\n1,duplicate\n3,also-ok\n")

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "mixed", Path: path,
		Options: ImportOptions{HasHeader: true, SkipErrors: true},
	})
	if err != nil {
		t.Fatalf("import: %v", res.Error)
	}
	if res.Skipped != 1 || res.Rows != 2 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.FirstError, "行 2") {
		t.Fatalf("firstError = %q, want the file row number 2", res.FirstError)
	}
}

// And without skipErrors a mid-file failure still rolls the whole import back.
func TestImportFailureRollsBackEverything(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	total := importfile.StreamChunkSize + 10

	var sb strings.Builder
	sb.WriteString("id,name\n")
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&sb, "%d,name%d\n", i, i)
	}
	// A bad row after the first chunk, in a NOT NULL column.
	sb.WriteString(",\n")
	path := filepath.Join(t.TempDir(), "rollback.csv")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	app.connections["c1"].DB.Exec(`CREATE TABLE rb (id integer primary key, name text not null)`)
	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "rb", Path: path,
		Options: ImportOptions{HasHeader: true},
	})
	if err == nil || res.Error == "" {
		t.Fatalf("import should fail: %+v", res)
	}
	var count int
	if err := app.connections["c1"].DB.QueryRow(`SELECT COUNT(*) FROM rb`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back import left %d rows", count)
	}
}
