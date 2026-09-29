package main

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// openWorkbook reads back what the export produced, so the tests assert on the
// file Excel would open rather than on the calls that made it.
func openWorkbook(t *testing.T, columns []WorkbookColumn, rows [][]interface{}) *excelize.File {
	t.Helper()
	data, err := buildWorkbook(columns, rows)
	if err != nil {
		t.Fatalf("build workbook: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open workbook: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func cellRaw(t *testing.T, f *excelize.File, cell string) string {
	t.Helper()
	value, err := f.GetCellValue(workbookSheetName, cell, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatalf("raw value %s: %v", cell, err)
	}
	return value
}

func cellText(t *testing.T, f *excelize.File, cell string) string {
	t.Helper()
	value, err := f.GetCellValue(workbookSheetName, cell)
	if err != nil {
		t.Fatalf("value %s: %v", cell, err)
	}
	return value
}

// A spreadsheet is only worth having over CSV if the cells keep their kinds: a
// number that can be summed, a date that can be sorted, a code that keeps its
// leading zeros, and NULL that stays an empty cell.
func TestWorkbookCellsKeepTheirTypes(t *testing.T) {
	columns := []WorkbookColumn{
		{Name: "id", Type: "INTEGER"},
		{Name: "code", Type: "TEXT"},
		{Name: "price", Type: "DECIMAL(10,2)"},
		{Name: "active", Type: "BOOLEAN"},
		{Name: "born", Type: "DATE"},
		{Name: "stamp", Type: "DATETIME"},
		{Name: "score", Type: "INTEGER"},
	}
	rows := [][]interface{}{
		{float64(7), "007", "12.50", true, "2026-09-22", "2026-09-22T08:30:00Z", nil},
		// The way a server that keeps no zone writes it, fraction included: the
		// layouts have to cover that too or the cell silently stays text.
		{float64(8), "8", "1", false, "2026-09-22 00:00:00", "2026-09-22 08:30:00.1234567", "N/A"},
	}

	f := openWorkbook(t, columns, rows)

	if got := cellText(t, f, "A1"); got != "id" {
		t.Errorf("header A1 = %q", got)
	}
	// Numeric columns arrive as text from some drivers; the declared type is
	// what turns "12.50" back into a number Excel can total.
	if got := cellRaw(t, f, "C2"); got != "12.5" {
		t.Errorf("price raw = %q, want 12.5", got)
	}
	if got, want := cellRaw(t, f, "B2"), "007"; got != want {
		t.Errorf("code = %q, want %q", got, want)
	}
	if kind, err := f.GetCellType(workbookSheetName, "B2"); err != nil {
		t.Fatalf("code type: %v", err)
	} else if kind == excelize.CellTypeNumber || kind == excelize.CellTypeFormula {
		t.Errorf("code cell type = %v, want a text cell", kind)
	}
	if kind, err := f.GetCellType(workbookSheetName, "D2"); err != nil {
		t.Fatalf("active type: %v", err)
	} else if kind != excelize.CellTypeBool {
		t.Errorf("active cell type = %v, want bool", kind)
	}
	// NULL is an empty cell, not the text "null" and not 0.
	if got := cellText(t, f, "G2"); got != "" {
		t.Errorf("score = %q, want an empty cell", got)
	}

	// Dates go in as the serial number Excel stores, with a format that spells
	// the date out the way the grid showed it rather than a locale default.
	for _, cell := range []string{"E2", "F2"} {
		raw := cellRaw(t, f, cell)
		if _, err := strconv.ParseFloat(raw, 64); err != nil {
			t.Fatalf("%s raw = %q, want a date serial number", cell, raw)
		}
		if shown := cellText(t, f, cell); shown == raw {
			t.Errorf("%s has no date format applied: %q", cell, shown)
		}
	}
	if shown := cellText(t, f, "E2"); shown != "2026-09-22" {
		t.Errorf("born shows %q, want 2026-09-22", shown)
	}
	if shown := cellText(t, f, "F2"); shown != "2026-09-22 08:30:00" {
		t.Errorf("stamp shows %q, want 2026-09-22 08:30:00", shown)
	}
	// A datetime at midnight is still a date, and the fraction is the format's
	// business, not the stored value's.
	if shown := cellText(t, f, "E3"); shown != "2026-09-22" {
		t.Errorf("born shows %q, want 2026-09-22", shown)
	}
	if shown := cellText(t, f, "F3"); shown != "2026-09-22 08:30:00" {
		t.Errorf("stamp shows %q, want 2026-09-22 08:30:00", shown)
	}
}

// The declared type is now read once per column instead of once per cell, so the
// mapping is worth pinning on its own - spelling, precision suffix and the MySQL
// YEAR that counts as a number rather than a date.
func TestWorkbookCellKindFollowsTheDeclaredType(t *testing.T) {
	cases := []struct {
		colType string
		want    cellKind
	}{
		{"int4", kindNumber},
		{"DECIMAL(10,2)", kindNumber},
		{"bigint unsigned", kindNumber},
		{"year", kindNumber},
		{"date", kindDate},
		{"DATETIME", kindDate},
		{"TIMESTAMP(6)", kindDate},
		{"timestamp with time zone", kindDate},
		{"varchar(200)", kindText},
		{"", kindText},
	}
	for _, tc := range cases {
		if got := workbookCellKind(tc.colType); got != tc.want {
			t.Errorf("workbookCellKind(%q) = %d, want %d", tc.colType, got, tc.want)
		}
	}
}

// Text is text: a value that looks like a formula or a number must not be
// reinterpreted by whatever opens the file.
func TestWorkbookTextCellsStayText(t *testing.T) {
	columns := []WorkbookColumn{
		{Name: "公式", Type: "TEXT"},
		{Name: "电话", Type: "TEXT"},
		{Name: "备注", Type: "TEXT"},
		{Name: "超长", Type: "TEXT"},
	}
	long := strings.Repeat("数", 80)
	f := openWorkbook(t, columns, [][]interface{}{
		{"=1+1", "000123", strings.Repeat("中", 25), long},
	})

	if got := cellText(t, f, "A1"); got != "公式" {
		t.Errorf("header = %q", got)
	}
	if formula, err := f.GetCellFormula(workbookSheetName, "A2"); err != nil {
		t.Fatalf("formula: %v", err)
	} else if formula != "" {
		t.Errorf("A2 became the formula %q", formula)
	}
	if got := cellText(t, f, "A2"); got != "=1+1" {
		t.Errorf("A2 = %q, want the literal text", got)
	}
	if got := cellText(t, f, "B2"); got != "000123" {
		t.Errorf("B2 = %q, want the leading zeros kept", got)
	}

	// Widths are guessed from how wide the values look: a CJK glyph counts double
	// so Chinese text gets room, and the guess is clamped so no column runs off
	// the screen.
	if width, err := f.GetColWidth(workbookSheetName, "C"); err != nil {
		t.Fatalf("note width: %v", err)
	} else if width < 50 {
		t.Errorf("width for 25 Chinese glyphs = %v, want it counted as double width", width)
	}
	if width, err := f.GetColWidth(workbookSheetName, "D"); err != nil {
		t.Fatalf("long width: %v", err)
	} else if width > workbookMaxColWidth {
		t.Errorf("width = %v, want it clamped to %v", width, workbookMaxColWidth)
	}
}

// A value only becomes a date or number cell when its column says so: a stamp
// kept in a text column, and junk in a numeric one, both stay as written rather
// than being reinterpreted - or refusing the whole file.
func TestWorkbookKeepsLookAlikeValuesAsTheyAre(t *testing.T) {
	columns := []WorkbookColumn{
		{Name: "快照", Type: "TEXT"},
		{Name: "数量", Type: "INTEGER"},
		{Name: "自由", Type: ""},
	}
	f := openWorkbook(t, columns, [][]interface{}{
		{"2026-09-22", "N/A", "42"},
	})

	if got := cellRaw(t, f, "A2"); got != "2026-09-22" {
		t.Errorf("A2 raw = %q, want the text left alone", got)
	}
	if got := cellText(t, f, "B2"); got != "N/A" {
		t.Errorf("B2 = %q, want the unparseable number kept visible", got)
	}
	if got := cellRaw(t, f, "C2"); got != "42" {
		t.Errorf("C2 raw = %q, want text for a column with no type", got)
	}
}

// The header row is the one thing a reader needs to stay put while scrolling a
// page of results, so it is bold and frozen.
func TestWorkbookHeaderIsBoldAndFrozen(t *testing.T) {
	f := openWorkbook(t,
		[]WorkbookColumn{{Name: "id", Type: "INTEGER"}, {Name: "name", Type: "TEXT"}},
		[][]interface{}{{float64(1), "a"}})

	styleID, err := f.GetCellStyle(workbookSheetName, "A1")
	if err != nil {
		t.Fatalf("header style: %v", err)
	}
	style, err := f.GetStyle(styleID)
	if err != nil {
		t.Fatalf("header font: %v", err)
	}
	if style.Font == nil || !style.Font.Bold {
		t.Error("header A1 is not bold")
	}
	// The last column has to be styled too, or the header looks half done.
	lastStyle, err := f.GetCellStyle(workbookSheetName, "B1")
	if err != nil {
		t.Fatalf("last header style: %v", err)
	}
	if lastStyle != styleID {
		t.Errorf("B1 style = %d, want the same %d", lastStyle, styleID)
	}

	panes, err := f.GetPanes(workbookSheetName)
	if err != nil {
		t.Fatalf("panes: %v", err)
	}
	if !panes.Freeze || panes.YSplit != 1 {
		t.Errorf("panes = %+v, want the first row frozen", panes)
	}
}

// A row that does not line up with the header would silently write a shifted
// record, so the file is refused and the row is named.
func TestWorkbookRefusesRowsThatDoNotLineUp(t *testing.T) {
	_, err := buildWorkbook(
		[]WorkbookColumn{{Name: "id", Type: "INTEGER"}, {Name: "name", Type: "TEXT"}},
		[][]interface{}{{float64(1), "a"}, {float64(2)}})
	if err == nil || !strings.Contains(err.Error(), "第 2 行有 1 个值，和 2 列对不上") {
		t.Fatalf("want a refusal naming the short row, got: %v", err)
	}
}

func TestWorkbookBase64Guardrails(t *testing.T) {
	app := &App{}
	one := []WorkbookColumn{{Name: "id", Type: "INTEGER"}}
	rows := [][]interface{}{{float64(1)}}

	if _, err := app.WorkbookBase64(nil, rows); err == nil ||
		!strings.Contains(err.Error(), "没有列") {
		t.Errorf("want a refusal without columns, got: %v", err)
	}
	if _, err := app.WorkbookBase64(one, nil); err == nil ||
		!strings.Contains(err.Error(), "没有可导出的行") {
		t.Errorf("want a refusal for an empty result, got: %v", err)
	}

	oversized := make([][]interface{}, workbookMaxRows+1)
	for i := range oversized {
		oversized[i] = []interface{}{i}
	}
	if _, err := app.WorkbookBase64(one, oversized); err == nil ||
		!strings.Contains(err.Error(), "一次最多导出") {
		t.Errorf("want a refusal above the cap, got: %v", err)
	}

	encoded, err := app.WorkbookBase64(one, rows)
	if err != nil {
		t.Fatalf("workbook: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// An xlsx file is a zip archive; the signature is what lets the browser save
	// bytes the client cannot inspect.
	if len(data) < 4 || string(data[:2]) != "PK" {
		t.Fatalf("want zip bytes, got %q", data[:min(4, len(data))])
	}
}
