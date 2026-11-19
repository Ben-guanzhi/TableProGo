package main

// 数据往返完整性（§332）：导出 → 导入 → 数据一致是客户端的核心正确性
// 承诺。此前各环节单测过，但「SQL INSERT 导出 → 脚本切分 → 执行 → 查询
// 比对」的闭环从未验证过。本测试用边界数据（NULL/引号/换行/Unicode/
// 大数/负数）跑完整链路。

import (
	"context"
	"fmt"
	"strings"
	"testing"

	dbcoresqlite "github.com/Ben-guanzhi/dbcore/sqlite"
	"github.com/Ben-guanzhi/dbcore/sqltext"
)

func TestInsertExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()

	// 源表：边界数据形状（NULL、引号、换行、中文、大数、负数、浮点）。
	type row struct {
		id    int64
		label string
		amt   string
		note  string
	}
	src := []row{
		{1, "plain", "10.50", "note"},
		{2, `quo"te`, "-99.99", `it's`},
		{3, "新\n行", "0.00", "中文中文"},
		{4, "", "123456789012345678901234567890", ""}, // 超过 float64 精度
		{5, "NULL-like", "", "\\backslash\\"},         // 空串 vs NULL 由 NumericColumnLiteral 区分
	}

	// 1) 建源表并灌数据。
	drv, err := dbcoresqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer drv.Close()
	if _, err := drv.Execute(ctx,
		"CREATE TABLE src (id INTEGER PRIMARY KEY, label TEXT, amt DECIMAL(38,6), note TEXT)"); err != nil {
		t.Fatal(err)
	}
	var scriptRows [][]interface{}
	for _, r := range src {
		if _, err := drv.Execute(ctx, fmt.Sprintf(
			"INSERT INTO src VALUES (%d, %s, %s, %s)",
			r.id,
			sqltext.QuotedLiteral("sqlite", r.label),
			sqltext.QuotedLiteral("sqlite", r.amt),
			sqltext.QuotedLiteral("sqlite", r.note),
		)); err != nil {
			t.Fatalf("seed %d: %v", r.id, err)
		}
		scriptRows = append(scriptRows, []interface{}{
			float64(r.id), r.label, r.amt, r.note,
		})
	}

	// 2) 导出为 INSERT 脚本（生产同款调用）。
	columns := []string{"id", "label", "amt", "note"}
	types := map[string]string{
		"id": "INTEGER", "label": "TEXT", "amt": "DECIMAL(38,6)", "note": "TEXT",
	}
	script, err := insertScript("sqlite", `"dst"`, "dst", columns, types, scriptRows)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// 3) 导入：切分脚本并在目标表执行。
	if _, err := drv.Execute(ctx,
		"CREATE TABLE dst (id INTEGER PRIMARY KEY, label TEXT, amt DECIMAL(38,6), note TEXT)"); err != nil {
		t.Fatal(err)
	}
	for i, stmt := range SplitSQLScript(script) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := drv.Execute(ctx, stmt); err != nil {
			t.Fatalf("import stmt %d (%s): %v", i, stmt[:min(len(stmt), 80)], err)
		}
	}

	// 4) 比对：源与目标的行逐行一致（按 id 排序）。
	//
	// 已知语义（save 路径的既有行为，导出与保存一致）：数字列的空串渲染为
	// NULL——用户清空数字格 = 清除值。SQLite 弱类型下"DECIMAL 列存了空串"
	// 的行，往返后 amt 变 NULL。这是有损点，测试按此断言；若未来要求严格
	// 无损，需给导出路径一个"保空串"的显式开关。
	want, _ := drv.Execute(ctx, "SELECT id,label,amt,note FROM src ORDER BY id")
	got, err := drv.Execute(ctx, "SELECT id,label,amt,note FROM dst ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Rows) != len(got.Rows) {
		t.Fatalf("row count: src=%d dst=%d", len(want.Rows), len(got.Rows))
	}
	for i, wr := range want.Rows {
		gr := got.Rows[i]
		for j, wv := range wr {
			wantStr := wv.String()
			if j == 2 && wantStr == "" {
				wantStr = "NULL" // the documented lossy point (numeric column, empty string)
			}
			if wantStr != gr[j].String() {
				t.Fatalf("row %d col %d: want=%q got=%q", i, j, wantStr, gr[j].String())
			}
		}
	}
}
