package main

import (
	"fmt"
	"html"
	"strings"
)

// renderHTML 把列名与值矩阵渲染为带样式的 HTML 表格（转义防注入）。
// renderLayoutHTML 按声明式布局渲染：列宽/对齐/主题 + 分组小计 + 汇总行。
func renderLayoutHTML(title string, spec layoutSpec, cols []string, rows [][]string) string {
	var b strings.Builder
	theme := spec.Theme
	if theme == "" {
		theme = "light"
	}
	bg, fg := "#fff", "#222"
	if theme == "dark" {
		bg, fg = "#1e1e1e", "#eaeaea"
	}
	fmt.Fprintf(&b, "<!doctype html><html><head><meta charset=\"utf-8\"><title>%s</title>", html.EscapeString(title))
	fmt.Fprintf(&b, "<style>body{background:%s;color:%s;font-family:sans-serif;margin:24px}table{border-collapse:collapse;width:100%%}th,td{border:1px solid #999;padding:6px 10px}h1{font-size:20px}.group{background:rgba(128,128,128,.15);font-weight:bold}.summary{background:rgba(128,128,128,.25);font-weight:bold}</style></head><body>", bg, fg)
	b.WriteString("<h1>" + html.EscapeString(title) + "</h1><table><thead><tr>")
	for i, c := range cols {
		style := ""
		if i < len(spec.Columns) {
			w, a := spec.Columns[i].Width, spec.Columns[i].Align
			if w != "" {
				style += "width:" + html.EscapeString(w) + ";"
			}
			if a != "" {
				style += "text-align:" + html.EscapeString(a) + ";"
			}
		}
		fmt.Fprintf(&b, "<th style=\"%s\">%s</th>", style, html.EscapeString(c))
	}
	b.WriteString("</tr></thead><tbody>")

	// 分组小计（按 layout.groups 指定的列索引分组）。
	groupIdx := map[int]bool{}
	for _, g := range spec.Groups {
		for i, lc := range spec.Columns {
			if strings.EqualFold(lc.Name, g) {
				groupIdx[i] = true
			}
		}
	}
	sumIdx := map[int]bool{}
	for _, s := range spec.Summary {
		for i, lc := range spec.Columns {
			if strings.EqualFold(lc.Name, s) {
				sumIdx[i] = true
			}
		}
	}

	var prevKey string
	var groupSum map[int]float64
	flush := func() {
		if groupSum == nil {
			return
		}
		b.WriteString("<tr class=\"group\"><td colspan=\"" + fmt.Sprint(len(cols)) + "\">小计</td>")
		for i := 0; i < len(cols); i++ {
			if sumIdx[i] {
				fmt.Fprintf(&b, "<td>%.2f</td>", groupSum[i])
			} else {
				b.WriteString("<td></td>")
			}
		}
		b.WriteString("</tr>")
	}
	total := make(map[int]float64)
	for _, r := range rows {
		key := ""
		for i := range groupIdx {
			if i < len(r) {
				key += r[i] + "|"
			}
		}
		if prevKey != "" && key != prevKey {
			flush()
			groupSum = nil
		}
		if groupSum == nil {
			groupSum = map[int]float64{}
		}
		b.WriteString("<tr>")
		for i, c := range r {
			b.WriteString("<td>" + html.EscapeString(c) + "</td>")
			if sumIdx[i] {
				var f float64
				fmt.Sscanf(strings.ReplaceAll(c, ",", ""), "%f", &f)
				groupSum[i] += f
				total[i] += f
			}
		}
		b.WriteString("</tr>")
		prevKey = key
	}
	flush()

	if len(sumIdx) > 0 {
		b.WriteString("<tr class=\"summary\"><td>合计</td>")
		for i := 0; i < len(cols); i++ {
			if sumIdx[i] {
				fmt.Fprintf(&b, "<td>%.2f</td>", total[i])
			} else {
				b.WriteString("<td></td>")
			}
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table><p>" + fmt.Sprintf("%d rows", len(rows)) + "</p></body></html>")
	return b.String()
}

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
