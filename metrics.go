package main

// 可用性指标（S-01）：契约在工作区根 `spec/usability-metrics.json`，实现共用
// `dbcore/usability`（默认关闭，POLYDB_USABILITY=1/true 才落盘）。
//
// 这一层只把「一次任务尝试」翻译成一个事件：成功/失败看 outcome，耗时看
// time.Since(started)，回退看 rollback 的 outcome=reverted。关闭时是 no-op。

import (
	"strings"
	"time"
	"unicode"
)

// recordTx 记一次事务任务（commit/rollback）。
func (a *App) recordTx(task string, started time.Time, err error) {
	if a == nil || a.um == nil {
		return
	}
	outcome := "ok"
	switch {
	case err != nil:
		outcome = "error"
	case task == "rollback":
		outcome = "reverted"
	}
	a.um.Record(task, outcome, time.Since(started), nil)
}

// stmtKeyword 取语句的首个关键字（SELECT/INSERT/…）作为 statement_type。
func stmtKeyword(sql string) string {
	s := strings.TrimLeftFunc(sql, func(r rune) bool {
		return unicode.IsSpace(r) || r == '(' || r == '/' || r == '*' || r == '-'
	})
	i := 0
	for i < len(s) && (s[i] == '_' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		i++
	}
	if i == 0 {
		return "unknown"
	}
	if i > 12 {
		i = 12
	}
	return strings.ToUpper(s[:i])
}
