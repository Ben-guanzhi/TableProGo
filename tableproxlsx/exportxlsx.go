// Package tableproxlsx turns the columns and rows a grid shows into an xlsx
// file.
//
// It is shared because two clients export the same grid: the parent module does
// it over its JSON API and the fyne client does it in-process. Keeping the
// rendering in one place keeps the two exports byte-identical, and there is a
// reason it has to be - a copy here that mapped an integer differently from the
// other one silently shipped primary keys as left-aligned text Excel would not
// sum.
package tableproxlsx

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/Ben-guanzhi/TableProGo/tableprosql"
)

// MaxRows bounds one xlsx export. The grid only ever holds a page of a
// table, so reaching this means the client sent something it should not have.
// This is ten times the INSERT-script cap: a workbook is a compressed archive,
// while a script is one INSERT per row of plain text that grows with the row
// count. The two caps differ on purpose, and each refusal names its own format.
const MaxRows = 100000

// SheetName is the one worksheet the export writes.
const SheetName = "查询结果"

// Column is one result column as the grid header reports it: the name to
// write in row 1 and the database type that decides what kind of cell the
// values become.
//
// The json tags matter: the parent module receives these over its HTTP API, so
// a caller must be able to send them as JSON.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// isDateColumn keeps the historical name; the type knowledge lives in the
// shared SQL text package's ColumnType value object, so every client answers
// "does this column hold a date" the same way. What the date then becomes in a
// cell is still this package's business.
func isDateColumn(colType string) bool {
	return tableprosql.NewColumnType(colType).IsDate()
}

// The layouts a result can arrive in. runSelect renders a driver's time as RFC
// 3339, so it comes first and covers both the offset and the fraction; the
// rest are dates and times the tables themselves hold as text, and a seconds
// layout also accepts a fraction the layout does not spell out.
var workbookTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02",
}

func parseWorkbookTime(text string) (time.Time, bool) {
	value := strings.TrimSpace(text)
	if len(value) < 8 {
		return time.Time{}, false
	}
	for _, layout := range workbookTimeLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// workbookStyles are the cell formats the export applies, built once per file
// because every style is a record in the workbook.
type workbookStyles struct {
	header int
	// The default date format is the reader's locale, which would give an
	// American "09-22-26" for a Chinese user's export. These say what is stored.
	dateOnly int
	dateTime int
}

// cellKind is what a column's values become in the sheet. The declared type says
// so once for the whole column, so it is worked out while the columns are walked
// rather than re-read from the type string per cell.
type cellKind int

const (
	kindText cellKind = iota
	kindNumber
	kindDate
)

// isNumericColumn keeps the grid's historical name; the type knowledge lives in
// the shared SQL text package's ColumnType value object, so every client answers
// "does this column hold numbers" the same way.
func isNumericColumn(colType string) bool {
	return tableprosql.NewColumnType(colType).IsNumeric()
}

func workbookCellKind(colType string) cellKind {
	switch {
	case isNumericColumn(colType):
		return kindNumber
	case isDateColumn(colType):
		return kindDate
	}
	return kindText
}

// asInt reports whether a value is one of the Go kinds a driver can hand back for
// an integer column, widened to int64. There is no single kind: SQLite and MySQL
// scan INTEGER into int64, PostgreSQL int4 into int32, and an unsigned column can
// come back as uint64. A switch here must cover every kind it wants to treat as
// a number - the one that was forgotten in a copy of this file once made primary
// keys export as left-aligned text that SUM() ignored.
func asInt(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint:
		return int64(v), true
	case uint8:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint32:
		return int64(v), true
	case uint64:
		return int64(v), true
	}
	return 0, false
}

// workbookCellValue is what one grid value becomes in the sheet. Numbers and
// dates become real cells; everything else goes in as text, which also keeps a
// value like "=1+1" or "007" from being reinterpreted by Excel. A nil comes
// back as nil so the row writer leaves a genuinely empty cell behind instead of
// spelling out "null".
func workbookCellValue(kind cellKind, value interface{}, styles workbookStyles) interface{} {
	switch v := value.(type) {
	case nil:
		return nil
	case bool:
		return v
	case float64:
		return v
	case string:
		switch kind {
		case kindNumber:
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				return nil
			}
			if number, err := strconv.ParseFloat(trimmed, 64); err == nil {
				return number
			}
			// Text in a numeric column is a display problem, not a reason to
			// refuse the whole file: keep it visible as it is.
			return v
		case kindDate:
			parsed, ok := parseWorkbookTime(v)
			if !ok {
				return v
			}
			style := styles.dateTime
			if parsed.Hour()|parsed.Minute()|parsed.Second() == 0 {
				style = styles.dateOnly
			}
			return excelize.Cell{Value: parsed, StyleID: style}
		}
		return v
	}
	if n, ok := asInt(value); ok {
		return n
	}
	return excelize.Cell{Value: fmt.Sprint(value)}
}

// workbookRow is one grid row in the shape the stream writer wants: a value per
// cell, in column order, so a whole row leaves memory with a single call.
func workbookRow(kinds []cellKind, row []interface{}, styles workbookStyles) []interface{} {
	cells := make([]interface{}, len(row))
	for i, value := range row {
		cells[i] = workbookCellValue(kinds[i], value, styles)
	}
	return cells
}

// displayUnits is how wide a value looks in a column, counted in the ASCII
// characters Excel sizes widths by. Anything from U+1100 up - CJK, Hangul, full
// width punctuation - is drawn about twice as wide as an "i", so it counts 2.
func displayUnits(text string) int {
	units := 0
	for _, r := range text {
		if r >= 0x1100 {
			units++
		}
		units++
	}
	return units
}

// cellDisplayUnits is displayUnits for a value the grid holds. A number counts
// as its shortest form; a date arrives as text and so counts as that text,
// which is a few characters more than the cell will show.
func cellDisplayUnits(value interface{}) int {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		return displayUnits(v)
	case bool:
		if v {
			return 5
		}
		return 4
	case float64:
		return len(strconv.FormatFloat(v, 'f', -1, 64))
	default:
		if n, ok := asInt(v); ok {
			return len(strconv.FormatInt(n, 10))
		}
		return displayUnits(fmt.Sprint(v))
	}
}

const (
	workbookMinColWidth = 9.0
	workbookMaxColWidth = 60.0
)

// newWorkbookStyles registers the three formats the export needs.
func newWorkbookStyles(f *excelize.File) (workbookStyles, error) {
	var styles workbookStyles
	header, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return styles, err
	}
	styles.header = header
	dateOnlyFormat := "yyyy-mm-dd"
	dateOnly, err := f.NewStyle(&excelize.Style{CustomNumFmt: &dateOnlyFormat})
	if err != nil {
		return styles, err
	}
	styles.dateOnly = dateOnly
	dateTimeFormat := "yyyy-mm-dd hh:mm:ss"
	dateTime, err := f.NewStyle(&excelize.Style{CustomNumFmt: &dateTimeFormat})
	if err != nil {
		return styles, err
	}
	styles.dateTime = dateTime
	return styles, nil
}

// BuildWorkbook renders the columns and rows on screen as an xlsx file: a bold,
// frozen header row, one typed cell per value, and a width per column.
//
// The row cap is enforced here and not left to each caller: both clients must
// refuse an oversized export, and a check that only some callers remember is no
// check at all.
func BuildWorkbook(columns []Column, rows [][]interface{}) ([]byte, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("没有列可导出")
	}
	if len(rows) > MaxRows {
		return nil, fmt.Errorf("一次最多导出 %d 行（Excel），请翻页或缩小范围", MaxRows)
	}
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	styles, err := newWorkbookStyles(f)
	if err != nil {
		return nil, err
	}
	sheet := f.GetSheetName(0)
	if err := f.SetSheetName(sheet, SheetName); err != nil {
		return nil, err
	}
	sheet = SheetName

	kinds := make([]cellKind, len(columns))
	widths := make([]int, len(columns))
	for i, col := range columns {
		kinds[i] = workbookCellKind(col.Type)
		// Seeded with the header so a short column of long names still fits.
		widths[i] = displayUnits(col.Name)
	}

	// The stream writer refuses a column width once the first row has been
	// written, so the widths are measured over the values first. A short row is
	// refused here too: it would otherwise write a shifted record.
	for r, row := range rows {
		if len(row) != len(columns) {
			return nil, fmt.Errorf("第 %d 行有 %d 个值，和 %d 列对不上", r+1, len(row), len(columns))
		}
		for i, value := range row {
			if w := cellDisplayUnits(value); w > widths[i] {
				widths[i] = w
			}
		}
	}

	// Rows stream to disk as they are made instead of being held in the workbook
	// in memory, so a big result does not carry its own size twice.
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return nil, err
	}
	if err := sw.SetPanes(&excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	}); err != nil {
		return nil, err
	}
	for i, width := range widths {
		// A cell is drawn with a little padding, and a width is only a guess at
		// how wide the value looks in the reader's font.
		size := min(max(float64(width+2), workbookMinColWidth), workbookMaxColWidth)
		if err := sw.SetColWidth(i+1, i+1, size); err != nil {
			return nil, err
		}
	}

	header := make([]interface{}, len(columns))
	for i, col := range columns {
		header[i] = excelize.Cell{Value: col.Name, StyleID: styles.header}
	}
	if err := sw.SetRow("A1", header); err != nil {
		return nil, err
	}
	for r, row := range rows {
		if err := sw.SetRow("A"+strconv.Itoa(r+2), workbookRow(kinds, row, styles)); err != nil {
			return nil, fmt.Errorf("第 %d 行：%w", r+1, err)
		}
	}
	if err := sw.Flush(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
