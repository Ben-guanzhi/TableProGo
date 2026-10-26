package main

import (
	"fmt"
	"io"

	"github.com/Ben-guanzhi/TableProGo/tableproxlsx"
)

// xlsxMimeType is the content type an xlsx download is served with.
const xlsxMimeType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// WorkbookTo streams the rows on screen into w as an xlsx file (P1-13).
//
// The old path returned the whole workbook base64-encoded inside a JSON body,
// which meant the server held it twice (the file plus its base64) and the
// client decoded it again. Streaming keeps exactly one full-size copy, in the
// response, and lets excelize write rows to disk as they are produced.
func (a *App) WorkbookTo(w io.Writer, columns []tableproxlsx.Column, rows [][]interface{}) error {
	if len(columns) == 0 {
		return fmt.Errorf("当前结果没有列，无法导出 XLSX")
	}
	if len(rows) == 0 {
		return fmt.Errorf("当前结果为空，没有可导出的行")
	}
	// Ten times the INSERT-script limit, the ratio the two formats always had:
	// a workbook is compressed, a script is one INSERT per row of plain text.
	if max := a.effectiveExportMaxRows() * 10; len(rows) > max {
		return fmt.Errorf("一次最多导出 %d 行（XLSX）：可在「设置」调整导出行数上限，或翻页/缩小范围", max)
	}
	return tableproxlsx.WriteWorkbook(w, columns, rows)
}
