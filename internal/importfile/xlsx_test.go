package importfile

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

type wbSheet struct {
	name string
	rows [][]string
}

// writeWorkbook builds an .xlsx fixture. The default sheet excelize creates is
// reused when a fixture names it, otherwise it is deleted so the workbook has
// exactly the requested sheets, in order.
func writeWorkbook(t *testing.T, sheets []wbSheet) string {
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
					t.Fatalf("set cell %s on %q: %v", coord, s.name, err)
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

// writeWorkbookBytes is writeWorkbook returned as bytes, for the reader path.
func writeWorkbookBytes(t *testing.T, sheets []wbSheet) *bytes.Buffer {
	t.Helper()
	path := writeWorkbook(t, sheets)
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("write buffer: %v", err)
	}
	return buf
}

func TestParseXLSXFileMultiSheet(t *testing.T) {
	path := writeWorkbook(t, []wbSheet{
		{name: "Sheet1", rows: [][]string{{"id", "name"}, {"1", "alice"}, {"2", "bob"}}},
		{name: "Staff Roster", rows: [][]string{{"id", "title"}, {"3", "engineer"}}},
	})
	sheets, err := ParseXLSXFile(path)
	if err != nil {
		t.Fatalf("ParseXLSXFile: %v", err)
	}
	if len(sheets) != 2 {
		t.Fatalf("sheets = %d, want 2", len(sheets))
	}
	if sheets[0].Name != "Sheet1" || sheets[1].Name != "Staff Roster" {
		t.Fatalf("names = %q, %q", sheets[0].Name, sheets[1].Name)
	}
	if len(sheets[0].Rows) != 2 || len(sheets[1].Rows) != 1 {
		t.Fatalf("row counts = %d, %d", len(sheets[0].Rows), len(sheets[1].Rows))
	}
	if *sheets[0].Rows[1][1] != "bob" {
		t.Fatalf("cell = %q", *sheets[0].Rows[1][1])
	}
}

func TestParseXLSXFileEmptyCellSemantics(t *testing.T) {
	// Column 2 of row 2 is present but blank (""), column 3 of row 2 is absent
	// (the row has no third cell), and row 3 is entirely blank. The parser
	// must keep "" as an empty string, absent as NULL (nil) and drop the blank
	// row.
	path := writeWorkbook(t, []wbSheet{{name: "Sheet1", rows: [][]string{
		{"id", "a", "b", "c"},
		{"1", "", "x", ""},
		{"", "", "", ""},
		{"2", "y"},
	}}})
	sheets, err := ParseXLSXFile(path)
	if err != nil {
		t.Fatalf("ParseXLSXFile: %v", err)
	}
	if len(sheets) != 1 || len(sheets[0].Rows) != 2 {
		t.Fatalf("rows = %+v", sheets[0].Rows)
	}
	row := sheets[0].Rows[0]
	if len(row) != 4 {
		t.Fatalf("row width = %d, want 4", len(row))
	}
	if row[1] == nil || *row[1] != "" {
		t.Fatalf("interior blank should be \"\", got %v", row[1])
	}
	if row[3] != nil {
		t.Fatalf("trailing absent cell should be NULL, got %q", *row[3])
	}
	row2 := sheets[0].Rows[1]
	if row2[1] == nil || *row2[1] != "y" {
		t.Fatalf("short row cell = %v", row2[1])
	}
	if row2[2] != nil {
		t.Fatalf("short row trailing cell should be NULL, got %v", row2[2])
	}
}

func TestParseXLSXFileSyntheticHeader(t *testing.T) {
	path := writeWorkbook(t, []wbSheet{{name: "Sheet1", rows: [][]string{
		{"", "City"},
		{"1", "x"},
	}}})
	sheets, _ := ParseXLSXFile(path)
	if sheets[0].Cols[0] != "column1" || sheets[0].Cols[1] != "City" {
		t.Fatalf("cols = %v", sheets[0].Cols)
	}
}

func TestParseXLSXEmptySheet(t *testing.T) {
	path := writeWorkbook(t, []wbSheet{
		{name: "Sheet1", rows: [][]string{}},
		{name: "Leads", rows: [][]string{{"id"}, {"1"}}},
	})
	sheets, err := ParseXLSXFile(path)
	if err != nil {
		t.Fatalf("ParseXLSXFile: %v", err)
	}
	if !sheets[0].Empty() {
		t.Fatalf("empty sheet should be Empty()")
	}
	if sheets[1].Empty() || sheets[1].Rows[0][0] == nil {
		t.Fatalf("non-empty sheet = %+v", sheets[1])
	}
}

func TestParseXLSXReader(t *testing.T) {
	buf := writeWorkbookBytes(t, []wbSheet{{name: "Sheet1", rows: [][]string{{"id"}, {"1"}}}})
	sheets, err := ParseXLSXReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ParseXLSXReader: %v", err)
	}
	if len(sheets) != 1 || *sheets[0].Rows[0][0] != "1" {
		t.Fatalf("sheets = %+v", sheets)
	}
}

func TestNormalizeDetectsExcel(t *testing.T) {
	tests := map[string]string{
		"book.xlsx":  "xlsx",
		"book.XLSX":  "xlsx",
		"legacy.xls": "xlsx",
		"macro.xlsm": "xlsx",
		"data.csv":   "csv",
	}
	for path, want := range tests {
		got := Normalize(Options{}, path)
		if got.Format != want {
			t.Errorf("Normalize(%q).Format = %q, want %q", path, got.Format, want)
		}
	}
}

func TestParseXLSXFileFirstSheet(t *testing.T) {
	path := writeWorkbook(t, []wbSheet{
		{name: "Sheet1", rows: [][]string{{"id", "name"}, {"1", "alice"}}},
		{name: "Second", rows: [][]string{{"id"}, {"2"}}},
	})
	cols, rows, err := Parse(path, Options{Format: "xlsx"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cols) != 2 || len(rows) != 1 || *rows[0][0] != "1" {
		t.Fatalf("cols=%v rows=%d", cols, len(rows))
	}
}
