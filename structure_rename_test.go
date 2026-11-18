package main

// 列改名走内核 RenameColumn——以前改名会被静默丢弃（只改名不改其它属性时
// AlterColumn 全 false 返回 nil）或对新名 ALTER 旧列（改名+改类型时）。

import (
	"strings"
	"testing"
)

func TestStructureEditRenamesColumn(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])

	// 只改名：必须出一条 RENAME COLUMN，而不是静默丢弃。
	res, err := app.PreviewStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "parts",
		Columns: []ColumnEdit{
			{Name: "id", Type: "INTEGER", Nullable: true},
			{Name: "part_name", Original: "name", Type: "TEXT", DefaultValue: "'x'"}, // name → part_name
		},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	joined := strings.Join(res.Statements, "\n")
	if !strings.Contains(joined, `RENAME COLUMN "name" TO "part_name"`) {
		t.Fatalf("missing rename: %v", res.Statements)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}

	// 没有 Original 的改名行（UI 不会发，但作为保守边界）：走 drop+add
	// ——身份未知时不能猜成 RENAME。

	// 改名 + 改类型：RENAME 在前，ALTER 用新名（不是旧名）。
	res, err = app.PreviewStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "parts",
		Columns: []ColumnEdit{
			{Name: "id", Type: "INTEGER", Nullable: true},
			{Name: "part_name", Original: "name", Type: "TEXT NOT NULL", DefaultValue: "'x'"},
		},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	joined = strings.Join(res.Statements, "\n")
	if !strings.Contains(joined, `RENAME COLUMN "name" TO "part_name"`) {
		t.Fatalf("rename missing in combined change: %v", res.Statements)
	}
	// The test runs on SQLite, which cannot ALTER a column's type in place
	// (CanAlterColumn=false): the type change must surface as a WARNING naming
	// the column — never silently dropped, never rendered against the old name.
	if strings.Contains(joined, `ALTER COLUMN "name"`) {
		t.Fatalf("alter must not target the OLD name: %v", res.Statements)
	}
	foundGap := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "part_name") && strings.Contains(w, "重建") {
			foundGap = true
		}
	}
	if !foundGap {
		t.Fatalf("SQLite 的类型改动应以 Warning 上浮（列 part_name）: %v / %v", res.Warnings, res.Statements)
	}
}
