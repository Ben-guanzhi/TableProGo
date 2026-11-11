package importfile

// XLSX reading lives in this package, not in tableproxlsx: the exporter there is
// deliberately byte-stable across clients and exposes no reading API, so a
// reading path there would drag its frozen contract along.

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// XLSXSheet is one worksheet parsed into the same (cols, rows) shape the CSV and
// JSON paths produce, so the writer, InsertSQL, MatchColumns, transaction and
// skipErrors handling are shared unchanged.
type XLSXSheet struct {
	Name string
	Cols []string
	Rows [][]*string
}

// Empty reports whether the worksheet carries no columns (and so no rows).
func (s XLSXSheet) Empty() bool { return len(s.Cols) == 0 }

// ParseXLSXFile parses every worksheet of an .xlsx/.xlsm workbook.
func ParseXLSXFile(path string) ([]XLSXSheet, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("import: 打开 Excel 文件失败：%w", err)
	}
	defer f.Close()
	return parseXLSX(f)
}

// ParseXLSXReader parses a workbook held in memory. The preview endpoint and the
// tests use it; the import path reads from disk.
func ParseXLSXReader(r io.Reader) ([]XLSXSheet, error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return nil, fmt.Errorf("import: 打开 Excel 内容失败：%w", err)
	}
	defer f.Close()
	return parseXLSX(f)
}

func parseXLSX(f *excelize.File) ([]XLSXSheet, error) {
	names := f.GetSheetList()
	sheets := make([]XLSXSheet, 0, len(names))
	for _, name := range names {
		raw, err := f.GetRows(name)
		if err != nil {
			return nil, fmt.Errorf("import: 读取工作表 %q 失败：%w", name, err)
		}
		sheet, err := xlsxSheet(name, raw, true)
		if err != nil {
			return nil, err
		}
		sheets = append(sheets, sheet)
	}
	return sheets, nil
}

// xlsxSheet turns one worksheet's raw cell grid into (cols, rows).
//
// Cell presence: excelize's streaming reader keeps interior blank cells as ""
// but drops trailing ones, so a cell that exists and is blank becomes "" (an
// empty string, not NULL) while a cell the row does not have — a short row, or
// one past its last populated cell — becomes nil (NULL). That mirrors CSV,
// where an absent field is NULL and an empty field is "".
func xlsxSheet(name string, raw [][]string, hasHeader bool) (XLSXSheet, error) {
	if len(raw) == 0 {
		return XLSXSheet{Name: name}, nil
	}
	width := 0
	for _, r := range raw {
		if len(r) > width {
			width = len(r)
		}
	}
	if width == 0 {
		return XLSXSheet{Name: name}, nil
	}
	cols := make([]string, width)
	if hasHeader {
		header := raw[0]
		for i := range cols {
			if i < len(header) {
				if v := strings.TrimSpace(header[i]); v != "" {
					cols[i] = v
					continue
				}
			}
			cols[i] = "column" + strconv.Itoa(i+1)
		}
	} else {
		for i := range cols {
			cols[i] = "column" + strconv.Itoa(i+1)
		}
	}
	rows := make([][]*string, 0, len(raw))
	start := 0
	if hasHeader {
		start = 1
	}
	for _, r := range raw[start:] {
		values := make([]*string, width)
		present := false
		for i := 0; i < width; i++ {
			if i < len(r) {
				v := r[i]
				values[i] = &v
				if v != "" {
					present = true
				}
			}
		}
		if !present {
			continue // a fully blank row, like a blank line in a CSV
		}
		rows = append(rows, values)
	}
	return XLSXSheet{Name: name, Cols: cols, Rows: rows}, nil
}

// SanitizeSheetName turns a worksheet name into a usable default table name:
// Excel forbids []:*?/\ in sheet names and caps them at 31 characters, so
// separators become "_", non-identifier characters are dropped and the result
// is lowercased.
func SanitizeSheetName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "sheet"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	out = strings.Trim(out, "_")
	if out == "" {
		return "sheet"
	}
	return out
}
