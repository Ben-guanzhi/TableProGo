package main

import (
	"fmt"
	"strings"
)

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
