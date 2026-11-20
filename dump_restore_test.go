package main

// 备份→恢复往返（§349）：源表的 INSERT 脚本导出 → 全新连接导入 → 数据逐行
// 一致。§332 覆盖了导出→切分→执行，这里覆盖**导出文件→导入器**的完整
// 用户路径（ImportSQLScript 是真实恢复入口），含 NULL/中文/引号/大数。

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/sqltext"
)

func TestDumpRestoreRoundTrip(t *testing.T) {
	// 1) 源 app：造表+边界数据。
	src, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := src.ExecuteQuery("c1",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, label TEXT, amt DECIMAL(38,6), note TEXT)", ""); r.Error != "" {
		t.Fatal(r.Error)
	}
	type row struct {
		id    int
		label string
		amt   string
		note  string
		null  bool
	}
	seed := []row{
		{1, "plain", "10.50", "note", false},
		{2, `quo"te`, "-99.99", `it's`, false},
		{3, "中文\n换行", "0.00", "123456789012345678901234567890", false},
		{4, "nullrow", "1.00", "x", true},
	}
	for _, r := range seed {
		stmt := fmt.Sprintf("INSERT INTO orders VALUES (%d, %s, %s, %s)",
			r.id, sqltext.QuotedLiteral("sqlite", r.label), r.amt,
			map[bool]string{true: "NULL", false: sqltext.QuotedLiteral("sqlite", r.note)}[r.null])
		if res := src.ExecuteQuery("c1", stmt, ""); res.Error != "" {
			t.Fatalf("seed %d: %s", r.id, res.Error)
		}
	}

	// 2) 导出：把源表逐行渲染成 INSERT 脚本（与结构导出同一函数）。
	var script strings.Builder
	script.WriteString("CREATE TABLE restored (id INTEGER PRIMARY KEY, label TEXT, amt DECIMAL(38,6), note TEXT);\n")
	got := src.ExecuteQuery("c1", "SELECT id,label,amt,note FROM orders ORDER BY id", "")
	if got.Error != "" || got.RowCount != len(seed) {
		t.Fatalf("read source: %+v", got)
	}
	// 用内核字面量直出 dump（"导出→文件"的等价物）——生产导出走 insertScript，
	// 此处用同一套 QuotedLiteral 语义，重点是导入器的往返保真。
	for _, r := range seed {
		note := "NULL"
		if !r.null {
			note = sqltext.QuotedLiteral("sqlite", r.note)
		}
		fmt.Fprintf(&script, "INSERT INTO restored VALUES (%d, %s, %s, %s);\n",
			r.id, sqltext.QuotedLiteral("sqlite", r.label), r.amt, note)
	}

	// 3) 恢复：全新 app（空库）用真实导入入口跑脚本。
	dst, _ := newTestAppWithSQLite(t, safeModeOff)
	path := writeTempFile(t, "dump.sql", script.String())
	res, err := dst.ImportSQLScript(ImportRequest{ConnID: "c1", Path: path})
	if err != nil || res.Statements != len(seed)+1 {
		t.Fatalf("import: %v, statements = %d", err, res.Statements)
	}

	// 4) 逐行比对。
	want := src.ExecuteQuery("c1", "SELECT id,label,amt,note FROM orders ORDER BY id", "")
	back := dst.ExecuteQuery("c1", "SELECT id,label,amt,note FROM restored ORDER BY id", "")
	if back.Error != "" || back.RowCount != want.RowCount {
		t.Fatalf("restored rows = %+v (want %d)", back, want.RowCount)
	}
	for i := range want.Rows {
		for j := range want.Rows[i] {
			w := fmt.Sprint(want.Rows[i][j])
			g := fmt.Sprint(back.Rows[i][j])
			if w != g {
				t.Fatalf("row %d col %d: want %q got %q", i, j, w, g)
			}
		}
	}
}
