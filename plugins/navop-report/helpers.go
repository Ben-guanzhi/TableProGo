package main

import (
	"fmt"
	"html"
	"strings"
)

// renderHTML 把列名与值矩阵渲染为带样式的 HTML 表格（转义防注入）。
func renderHTML(title string, cols []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title><style>body{font-family:sans-serif;margin:24px}table{border-collapse:collapse;width:100%}th,td{border:1px solid #ddd;padding:6px 10px;text-align:left}th{background:#f5f5f5}h1{font-size:20px}</style></head><body>")
	b.WriteString("<h1>" + html.EscapeString(title) + "</h1><table><thead><tr>")
	for _, c := range cols {
		b.WriteString("<th>" + html.EscapeString(c) + "</th>")
	}
	b.WriteString("</tr></thead><tbody>")
	for _, r := range rows {
		b.WriteString("<tr>")
		for _, c := range r {
			b.WriteString("<td>" + html.EscapeString(c) + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table><p>" + fmt.Sprintf("%d rows", len(rows)) + "</p></body></html>")
	return b.String()
}

func renderReport(title string, cols []string, rows []map[string]any) string {
	var b strings.Builder
	b.WriteString("# " + title + "\n\n")
	b.WriteString("| " + strings.Join(cols, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(cols)) + "\n")
	for _, r := range rows {
		cells := make([]string, 0, len(cols))
		for _, c := range cols {
			cells = append(cells, fmt.Sprintf("%v", r[c]))
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	b.WriteString(fmt.Sprintf("\n共 %d 行\n", len(rows)))
	return b.String()
}
