package main

import (
	"encoding/base64"
	"fmt"

	"github.com/Ben-guanzhi/TableProGo/tableproxlsx"
)

// WorkbookBase64 turns the rows on screen into an xlsx file, returned base64
// encoded because the local API only carries JSON. Unlike the SQL export this
// works for any result - a view or a custom query has cells too.
//
// The rendering lives in tableproxlsx, shared with the fyne client, so the two
// exports stay identical. Only the refusals and the encoding are specific to
// this API.
func (a *App) WorkbookBase64(columns []tableproxlsx.Column, rows [][]interface{}) (string, error) {
	if len(columns) == 0 {
		return "", fmt.Errorf("当前结果没有列，无法导出 XLSX")
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("当前结果为空，没有可导出的行")
	}
	data, err := tableproxlsx.BuildWorkbook(columns, rows)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
