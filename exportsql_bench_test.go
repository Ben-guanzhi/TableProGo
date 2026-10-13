package main

import (
	"strings"
	"testing"
)

// benchExportRows builds a full export: the cap of rows over the columns a
// real table would carry - one integer key, two text columns (one of them
// carrying an apostrophe, because that is the case that pays the escape),
// one number that arrived as text, one NULL.
func benchExportRows(n int) ([]string, map[string]string, [][]interface{}) {
	cols := []string{"id", "name", "note", "score", "city"}
	types := map[string]string{
		"id": "integer", "name": "varchar(50)", "note": "text",
		"score": "decimal(10,2)", "city": "nvarchar(30)",
	}
	rows := make([][]interface{}, n)
	for i := range rows {
		rows[i] = []interface{}{
			float64(i),
			"customer",
			"It's here",
			"12.50",
			"上海浦东",
		}
		if i%3 == 0 {
			rows[i][4] = nil
		}
	}
	return cols, types, rows
}

func BenchmarkInsertScript(b *testing.B) {
	cols, types, rows := benchExportRows(insertExportMaxRows)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := insertScript("sqlite", `"parts"`, "parts", cols, types, rows)
		if err != nil {
			b.Fatal(err)
		}
		if !strings.HasPrefix(got, "INSERT INTO") {
			b.Fatal("empty script")
		}
	}
}

// BenchmarkInsertScriptOld runs the pre-builder renderer (kept in the test
// file as the equivalence oracle) over the same rows in the same process, so
// the two numbers are directly comparable without trusting absolute costs
// across runs.
func BenchmarkInsertScriptOld(b *testing.B) {
	cols, types, rows := benchExportRows(insertExportMaxRows)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := oldInsertScript("sqlite", `"parts"`, "parts", cols, types, rows)
		if err != nil {
			b.Fatal(err)
		}
		if !strings.HasPrefix(got, "INSERT INTO") {
			b.Fatal("empty script")
		}
	}
}
