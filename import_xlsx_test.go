package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

type xlsxSheet struct {
	name string
	rows [][]string
}

// writeXLSXFixture builds an .xlsx file from a worksheet grid. The default sheet
// excelize creates is reused when the first fixture names it, otherwise it is
// removed so the workbook has exactly the requested sheets.
func writeXLSXFixture(t *testing.T, sheets []xlsxSheet) string {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	defaultName := f.GetSheetName(0)
	for _, s := range sheets {
		if s.name != defaultName {
			if _, err := f.NewSheet(s.name); err != nil {
				t.Fatalf("new sheet %q: %v", s.name, err)
			}
		}
		for i, row := range s.rows {
			for j, v := range row {
				coord, err := excelize.CoordinatesToCellName(j+1, i+1)
				if err != nil {
					t.Fatalf("coord: %v", err)
				}
				if err := f.SetCellValue(s.name, coord, v); err != nil {
					t.Fatalf("set cell %s: %v", coord, err)
				}
			}
		}
	}
	if len(sheets) > 0 && sheets[0].name != defaultName {
		if err := f.DeleteSheet(defaultName); err != nil {
			t.Fatalf("delete default sheet: %v", err)
		}
	}
	path := filepath.Join(t.TempDir(), "book.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	return path
}

// TestImportXLSXSingleSheetCreatesTable pins the xlsx import path: the sheet is
// read into the same (cols, rows) shape as CSV, a blank interior cell becomes ""
// and a trailing absent cell becomes NULL.
func TestImportXLSXSingleSheetCreatesTable(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeXLSXFixture(t, []xlsxSheet{{name: "Sheet1", rows: [][]string{
		{"id", "name", "note"},
		{"1", "", "x"}, // interior blank stays ""
		{"2", "bob"},   // trailing absent cell stays NULL
	}}})

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "xl_people", Path: path,
		Options: ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !res.CreatedTable || res.Rows != 2 || len(res.Columns) != 3 {
		t.Fatalf("result = %+v", res)
	}
	r := app.ExecuteQuery("c1", "SELECT name, note FROM xl_people ORDER BY id", "")
	if r.Error != "" || r.RowCount != 2 {
		t.Fatalf("rows = %+v", r)
	}
	if got := fmt.Sprintf("%v|%v", r.Rows[0][0], r.Rows[0][1]); got != "|x" {
		t.Fatalf("row 1 = %q, want an empty name and note=x", got)
	}
	if got := fmt.Sprintf("%v|%v", r.Rows[1][0], r.Rows[1][1]); got != "bob|<nil>" {
		t.Fatalf("row 2 = %q, want bob with a NULL note", got)
	}
}

// TestImportXLSXMultiSheetOneTablePerSheet: with no sheet named, every worksheet
// becomes its own table named after the sheet.
func TestImportXLSXMultiSheetOneTablePerSheet(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeXLSXFixture(t, []xlsxSheet{
		{name: "Sheet1", rows: [][]string{{"id", "name"}, {"1", "alice"}}},
		{name: "Staff Roster", rows: [][]string{{"id", "title"}, {"3", "engineer"}}},
	})

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Path: path,
		Options: ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Rows != 2 {
		t.Fatalf("rows = %d, want 2", res.Rows)
	}
	if len(res.Tables) != 2 {
		t.Fatalf("tables = %+v", res.Tables)
	}
	if res.Tables[0].Table != "sheet1" || res.Tables[1].Table != "staff_roster" {
		t.Fatalf("table names = %q, %q", res.Tables[0].Table, res.Tables[1].Table)
	}
	if !res.Tables[0].CreatedTable || !res.Tables[1].CreatedTable {
		t.Fatalf("both tables should be created: %+v", res.Tables)
	}
	r := app.ExecuteQuery("c1", "SELECT name FROM sheet1", "")
	if r.Error != "" || r.RowCount != 1 || fmt.Sprintf("%v", r.Rows[0][0]) != "alice" {
		t.Fatalf("sheet1 rows = %+v", r)
	}
}

// TestImportXLSXMultiSheetFailureIsolation: one worksheet that cannot be written
// (its target table has a NOT NULL column the sheet leaves NULL) fails alone —
// the other sheets are still committed, and the caller gets the successful units
// back with the failure in res.Error.
func TestImportXLSXMultiSheetFailureIsolation(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE bad (id INTEGER PRIMARY KEY, note TEXT NOT NULL)", ""); r.Error != "" {
		t.Fatalf("create bad: %s", r.Error)
	}
	path := writeXLSXFixture(t, []xlsxSheet{
		{name: "Sheet1", rows: [][]string{{"id", "name"}, {"1", "alice"}}},
		{name: "Sheet2", rows: [][]string{{"id", "note"}, {"5"}}},
	})

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Path: path,
		Sheets:  []SheetImport{{Sheet: "Sheet1", Table: "good"}, {Sheet: "Sheet2", Table: "bad"}},
		Options: ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Error == "" {
		t.Fatalf("the failing sheet must be reported in res.Error: %+v", res)
	}
	if res.Rows != 1 {
		t.Fatalf("rows = %d, want the surviving sheet's 1 row", res.Rows)
	}
	if len(res.Tables) != 2 {
		t.Fatalf("tables = %+v", res.Tables)
	}
	if res.Tables[0].Table != "good" || res.Tables[0].Error != "" {
		t.Fatalf("good unit = %+v, want it committed without error", res.Tables[0])
	}
	if res.Tables[1].Table != "bad" || res.Tables[1].Error == "" {
		t.Fatalf("bad unit = %+v, want it failed with an error", res.Tables[1])
	}

	r := app.ExecuteQuery("c1", "SELECT name FROM good", "")
	if r.Error != "" || r.RowCount != 1 || fmt.Sprintf("%v", r.Rows[0][0]) != "alice" {
		t.Fatalf("good rows = %+v", r)
	}
	// The failed sheet's own transaction rolled back, so nothing leaked.
	r = app.ExecuteQuery("c1", "SELECT COUNT(*) FROM bad", "")
	if r.Error != "" || fmt.Sprintf("%v", r.Rows[0][0]) != "0" {
		t.Fatalf("bad rows = %+v", r)
	}
}

// TestImportColumnMapIsAuthoritative: a non-empty columnMap is authoritative —
// unlisted file columns are skipped, not auto-matched — and it is applied before
// the INSERT, renaming a file column onto a different table column.
func TestImportColumnMapIsAuthoritative(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE auth_people (id INTEGER PRIMARY KEY, name TEXT, note TEXT)", ""); r.Error != "" {
		t.Fatalf("create: %s", r.Error)
	}
	path := writeTempFile(t, "people.csv", "id,name,extra\n1,alice,z\n")

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "auth_people", Path: path,
		ColumnMap: map[string]string{"id": "id", "name": "name"},
		Options:   ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Rows != 1 || res.Skipped != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Mapped) != 3 || res.Mapped[2].File != "extra" || res.Mapped[2].Table != "" {
		t.Fatalf("mapped = %+v, want extra reported as skipped", res.Mapped)
	}
	r := app.ExecuteQuery("c1", "SELECT id, name FROM auth_people", "")
	if r.Error != "" || r.RowCount != 1 || fmt.Sprintf("%v|%v", r.Rows[0][0], r.Rows[0][1]) != "1|alice" {
		t.Fatalf("rows = %+v", r)
	}
}

func TestImportColumnMapRejectsBadMappings(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE t_people (id INTEGER PRIMARY KEY, name TEXT)", ""); r.Error != "" {
		t.Fatalf("create: %s", r.Error)
	}
	path := writeTempFile(t, "people.csv", "id,name\n1,alice\n")
	tests := []struct {
		name    string
		colMap  map[string]string
		wantSub string
	}{
		{"unknown source", map[string]string{"nope": "id"}, "不在文件列中"},
		{"duplicate target", map[string]string{"id": "x", "name": "x"}, "被使用了多次"},
		{"all skipped", map[string]string{"id": "", "name": ""}, "没有可导入的列"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := app.ImportFile(ImportRequest{
				ConnID: "c1", Table: "t_people", Path: path, ColumnMap: tt.colMap,
				Options: ImportOptions{HasHeader: true, CreateTable: true},
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantSub)
			}
		})
	}
	// None of the rejected mappings wrote anything.
	r := app.ExecuteQuery("c1", "SELECT COUNT(*) FROM t_people", "")
	if r.Error != "" || fmt.Sprintf("%v", r.Rows[0][0]) != "0" {
		t.Fatalf("rows = %+v, want 0", r)
	}
}

// TestPreviewImportRendersSQLWithoutWriting: the preview endpoint resolves the
// mapping, renders CREATE/INSERT and never touches the database.
func TestPreviewImportRendersSQLWithoutWriting(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeXLSXFixture(t, []xlsxSheet{{name: "Sheet1", rows: [][]string{
		{"id", "name"},
		{"1", "alice"},
	}}})

	pv, err := app.PreviewImport(ImportRequest{
		ConnID: "c1", Table: "preview_people", Path: path,
		Options: ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(pv.Tables) != 1 || pv.Tables[0].Table != "preview_people" {
		t.Fatalf("preview = %+v", pv)
	}
	tp := pv.Tables[0]
	if len(tp.Columns) != 2 || !strings.Contains(tp.DDL, "CREATE TABLE") {
		t.Fatalf("table preview = %+v", tp)
	}
	if !strings.Contains(tp.Sample[0], "INSERT INTO") {
		t.Fatalf("sample = %v", tp.Sample)
	}
	if tp.Exists {
		t.Fatal("the preview must not create the table")
	}
	r := app.ExecuteQuery("c1", "SELECT name FROM sqlite_master WHERE type='table' AND name='preview_people'", "")
	if r.Error != "" || r.RowCount != 0 {
		t.Fatalf("the table should not exist, got %+v", r)
	}
}

// TestPreviewImportExistingTableReportsMapping: against an existing table the
// preview reports the auto-matched pairing and skips the file column the table
// does not have.
func TestPreviewImportExistingTableReportsMapping(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE pre_people (id INTEGER PRIMARY KEY, name TEXT)", ""); r.Error != "" {
		t.Fatalf("create: %s", r.Error)
	}
	path := writeTempFile(t, "people.csv", "Name,ID,extra\ncarol,3,z\n")

	pv, err := app.PreviewImport(ImportRequest{
		ConnID: "c1", Table: "pre_people", Path: path,
		Options: ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	tp := pv.Tables[0]
	if !tp.Exists || tp.DDL != "" {
		t.Fatalf("table = %+v, want it to exist with no DDL", tp)
	}
	if len(tp.Mapped) != 3 || tp.Mapped[2].Table != "" {
		t.Fatalf("mapped = %+v", tp.Mapped)
	}
	if !strings.Contains(tp.Sample[0], "INSERT INTO") {
		t.Fatalf("sample = %v", tp.Sample)
	}
}

// TestImportXLSXSkipColumnsDropsThemFromTheTable: SkipColumns are dropped before
// CREATE TABLE, so the skipped column never reaches the target.
func TestImportXLSXSkipColumnsDropsThemFromTheTable(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeXLSXFixture(t, []xlsxSheet{{name: "Sheet1", rows: [][]string{
		{"id", "name", "extra"},
		{"1", "alice", "x"},
	}}})

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "skip_people", Path: path,
		SkipColumns: []string{"extra"},
		Options:     ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Rows != 1 {
		t.Fatalf("rows = %+v", res)
	}
	// The skipped column never reaches the target, even though the file column
	// list still reports it.
	r := app.ExecuteQuery("c1", "SELECT sql FROM sqlite_master WHERE type='table' AND name='skip_people'", "")
	if r.Error != "" || strings.Contains(fmt.Sprintf("%v", r.Rows[0][0]), "extra") {
		t.Fatalf("create sql = %v, want it without the skipped column", r.Rows[0][0])
	}
	r = app.ExecuteQuery("c1", "SELECT id, name FROM skip_people", "")
	if r.Error != "" || r.RowCount != 1 || fmt.Sprintf("%v|%v", r.Rows[0][0], r.Rows[0][1]) != "1|alice" {
		t.Fatalf("rows = %+v", r)
	}
}

// TestImportMultipleFiles: one request over several paths imports each file.
func TestImportMultipleFiles(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	first := writeTempFile(t, "a.csv", "id,name\n1,alice\n")
	second := writeTempFile(t, "b.csv", "id,name\n2,bob\n")

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "multi",
		Paths:   []string{first, second},
		Options: ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Rows != 2 || len(res.Tables) != 2 {
		t.Fatalf("result = %+v", res)
	}
	r := app.ExecuteQuery("c1", "SELECT name FROM multi ORDER BY id", "")
	if r.Error != "" || r.RowCount != 2 {
		t.Fatalf("rows = %+v", r)
	}
}

// TestPreviewImportExplicitMappingOnExistingTable: an authoritative mapping still
// reports that the target table exists and lists its columns, so the mapping
// editor can be filled in.
func TestPreviewImportExplicitMappingOnExistingTable(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE ex_people (id INTEGER PRIMARY KEY, name TEXT, note TEXT)", ""); r.Error != "" {
		t.Fatalf("create: %s", r.Error)
	}
	path := writeTempFile(t, "people.csv", "id,name,extra\n1,alice,z\n")

	pv, err := app.PreviewImport(ImportRequest{
		ConnID: "c1", Table: "ex_people", Path: path,
		ColumnMap: map[string]string{"id": "id", "name": "name", "extra": "note"},
		Options:   ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	tp := pv.Tables[0]
	if !tp.Exists {
		t.Fatalf("the authoritative path must still report the table: %+v", tp)
	}
	if len(tp.TargetColumns) != 3 || !slices.Contains(tp.TargetColumns, "note") {
		t.Fatalf("target columns = %v", tp.TargetColumns)
	}
	if len(tp.Mapped) != 3 || tp.Mapped[2].Table != "note" {
		t.Fatalf("mapped = %+v", tp.Mapped)
	}
}

// TestPreviewImportMultiSheetListsEveryTable: the multi-sheet preview reports one
// entry per worksheet and its sanitized table name.
func TestPreviewImportMultiSheetListsEveryTable(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeXLSXFixture(t, []xlsxSheet{
		{name: "Sheet1", rows: [][]string{{"id"}, {"1"}}},
		{name: "Staff Roster", rows: [][]string{{"id"}, {"2"}}},
	})
	pv, err := app.PreviewImport(ImportRequest{
		ConnID: "c1", Path: path,
		Options: ImportOptions{HasHeader: true, CreateTable: true},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(pv.Tables) != 2 {
		t.Fatalf("tables = %+v", pv.Tables)
	}
	if pv.Tables[1].Table != "staff_roster" {
		t.Fatalf("table name = %q", pv.Tables[1].Table)
	}
	if len(pv.SQL) == 0 {
		t.Fatal("the preview should collect the rendered SQL")
	}
	if !strings.Contains(strings.Join(pv.Notes, "\n"), "工作簿有 2 个工作表") {
		t.Fatalf("notes = %v", pv.Notes)
	}
}
