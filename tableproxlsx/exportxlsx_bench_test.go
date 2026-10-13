package tableproxlsx

import (
	"strconv"
	"testing"
)

// benchDateRows builds rows over the columns a browsed table really has: an
// integer key, a text name, and two date columns whose values arrive as the
// text runSelect leaves them - a bare DATE and a space-separated datetime,
// the two shapes that make parseWorkbookTime walk its layout list.
func benchDateRows(n int) ([]Column, [][]interface{}) {
	cols := []Column{
		{Name: "id", Type: "bigint"},
		{Name: "name", Type: "varchar"},
		{Name: "day", Type: "date"},
		{Name: "stamp", Type: "datetime"},
	}
	rows := make([][]interface{}, n)
	for i := range rows {
		rows[i] = []interface{}{
			int64(i),
			"customer-" + strconv.Itoa(i%97),
			"2026-02-03",
			"2026-02-03 14:05:06",
		}
	}
	return cols, rows
}

func BenchmarkBuildWorkbookDates(b *testing.B) {
	cols, rows := benchDateRows(20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := BuildWorkbook(cols, rows)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("empty workbook")
		}
	}
}

// BenchmarkWorkbookCellRow isolates the per-cell conversion the workbook
// pays before excelize ever sees a value: the widths sweep and the row
// writer both run it.
func BenchmarkWorkbookCellRow(b *testing.B) {
	cols, rows := benchDateRows(20000)
	kinds := make([]cellKind, len(cols))
	for i, col := range cols {
		kinds[i] = workbookCellKind(col.Type)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, row := range rows {
			if cells := workbookRow(kinds, row, workbookStyles{}); len(cells) != len(cols) {
				b.Fatal("short row")
			}
		}
	}
}

// BenchmarkWorkbookDateCellsOldWalk runs the pre-dispatch flat layout walk
// (kept in the test file as the equivalence oracle) over the same date cells
// in the same process, so the conversion difference is directly comparable.
func BenchmarkWorkbookDateCellsOldWalk(b *testing.B) {
	_, rows := benchDateRows(20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, row := range rows {
			if _, ok := oldParseWorkbookTime(row[2].(string)); !ok {
				b.Fatal("date refused")
			}
			if _, ok := oldParseWorkbookTime(row[3].(string)); !ok {
				b.Fatal("datetime refused")
			}
		}
	}
}

// BenchmarkWorkbookDateCellsNewDispatch runs the dispatched parse over the
// same cells in the same process.
func BenchmarkWorkbookDateCellsNewDispatch(b *testing.B) {
	_, rows := benchDateRows(20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, row := range rows {
			if _, ok := parseWorkbookTime(row[2].(string)); !ok {
				b.Fatal("date refused")
			}
			if _, ok := parseWorkbookTime(row[3].(string)); !ok {
				b.Fatal("datetime refused")
			}
		}
	}
}
