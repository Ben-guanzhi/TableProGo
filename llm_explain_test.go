package main

import (
	"strings"
	"testing"
)

// splitSQLSteps 的契约测试（Q-01）：分句切步、列提取、字符串字面量里的
// 关键词不切——proposal 卡的解释步依赖它。
func TestSplitSQLSteps(t *testing.T) {
	sql := "SELECT name, count(*) AS c FROM orders WHERE status = 'on hold' AND amount > 10 GROUP BY name ORDER BY c DESC LIMIT 10"
	steps := splitSQLSteps(sql)
	var clauses []string
	for _, s := range steps {
		clauses = append(clauses, s.Clause)
	}
	want := []string{"SELECT", "FROM", "WHERE", "GROUP BY", "ORDER BY", "LIMIT"}
	if len(steps) != len(want) {
		t.Fatalf("steps = %v, want %d 步", clauses, len(want))
	}
	for i := range want {
		if clauses[i] != want[i] {
			t.Errorf("step %d clause = %q, want %q", i, clauses[i], want[i])
		}
	}
	// SELECT 步引用的列含 name（前端 hover 高亮的输入）。
	foundName := false
	for _, c := range steps[0].Columns {
		if c == "name" {
			foundName = true
		}
	}
	if !foundName {
		t.Errorf("SELECT 步应引用 name 列: %v", steps[0].Columns)
	}
	// 字符串字面量里的 "where" 不应把 WHERE 步切碎——WHERE 步文本应完整。
	foundWhere := false
	for _, s := range steps {
		if s.Clause == "WHERE" {
			foundWhere = true
			if !strings.Contains(s.Text, "status = 'on hold'") {
				t.Errorf("WHERE 步文本不完整（字面量被切）: %q", s.Text)
			}
		}
	}
	if !foundWhere {
		t.Error("缺 WHERE 步")
	}
	// 空输入。
	if got := splitSQLSteps("  "); got != nil {
		t.Errorf("空 SQL 应返回 nil: %v", got)
	}
}
