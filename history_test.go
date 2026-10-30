package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newHistoryApp(t *testing.T) *App {
	t.Helper()
	return &App{historyPath: filepath.Join(t.TempDir(), "history.json")}
}

func TestStatementTableFollowsQueryShape(t *testing.T) {
	cases := map[string]string{
		"select * from dbo.GS_LOCK where id = 1":  "gs_lock",
		"UPDATE [dbo].[CP_StockBillType] SET f=1": "cp_stockbilltype",
		"-- from other\nselect 1 from `zs`.`t`":   "t",
		"select now()":                            "",
		"from 123":                                "",
	}
	for query, want := range cases {
		if got := statementTable(query); got != want {
			t.Errorf("statementTable(%q) = %q, want %q", query, got, want)
		}
	}
}

func TestHistoryFiltersAndOrdering(t *testing.T) {
	a := newHistoryApp(t)
	a.recordStatement("c1", "zs", "select * from gs_lock", QueryResult{RowCount: 3})
	a.recordStatement("c1", "zs", "select * from gs_user", QueryResult{AffectedRows: 1})
	a.recordStatement("c2", "other", "select * from gs_lock", QueryResult{Error: "boom"})

	all := a.History("", "", "", 0)
	if len(all) != 3 {
		t.Fatalf("history lost entries: %d", len(all))
	}
	if all[0].SQL != "select * from gs_lock" || all[0].Error != "boom" {
		t.Error("history must come back newest first")
	}
	if all[2].ID >= all[1].ID || all[1].ID >= all[0].ID {
		t.Error("ids must increase with time")
	}

	if got := a.History("c1", "zs", "GS_LOCK", 0); len(got) != 1 {
		t.Errorf("per-table filter returned %d entries", len(got))
	}
	if got := a.History("c1", "zs", "", 0); len(got) != 2 {
		t.Errorf("per-connection filter returned %d entries", len(got))
	}
	// A statement run without an explicit database stays visible in a
	// database-filtered list, because it used the connection's own database.
	a.recordStatement("c1", "", "select * from gs_lock", QueryResult{})
	if got := a.History("c1", "zs", "", 0); len(got) != 3 {
		t.Errorf("unqualified statements were hidden: %d", len(got))
	}
	if got := a.History("", "", "", 1); len(got) != 1 {
		t.Errorf("limit ignored: %d", len(got))
	}
}

func TestHistoryRoundTripsThroughFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	a := &App{historyPath: path}
	a.recordStatement("c1", "zs", "select * from gs_lock", QueryResult{RowCount: 2})

	b := &App{historyPath: path}
	b.loadHistory()
	entries := b.History("", "", "", 0)
	if len(entries) != 1 || entries[0].RowCount != 2 {
		t.Fatalf("history did not reload: %#v", entries)
	}
	// New ids must continue from the reloaded file, not collide with it.
	b.recordStatement("c1", "zs", "select 1", QueryResult{})
	if got := b.History("", "", "", 0)[0].ID; got != 2 {
		t.Errorf("id after reload = %d, want 2", got)
	}
}

func TestHistoryStaysBounded(t *testing.T) {
	a := newHistoryApp(t)
	for i := 0; i < historyLimit+50; i++ {
		a.recordStatement("c1", "zs", "select 1", QueryResult{})
	}
	if len(a.History("", "", "", 0)) != historyLimit {
		t.Errorf("history grew past the cap")
	}
	a.recordStatement("c1", "zs", "select '"+strings.Repeat("x", historySQLChars), QueryResult{})
	if last := a.History("", "", "", 1)[0]; len([]rune(last.SQL)) > historySQLChars+2 {
		t.Errorf("statement was not truncated: %d runes", len([]rune(last.SQL)))
	}
}

func TestClearHistoryHonoursFilters(t *testing.T) {
	a := newHistoryApp(t)
	a.recordStatement("c1", "zs", "select * from gs_lock", QueryResult{})
	a.recordStatement("c1", "zs", "select * from gs_user", QueryResult{})
	a.recordStatement("c2", "zs", "select * from gs_lock", QueryResult{})

	if removed := a.ClearHistory("c1", "zs", "gs_lock"); removed != 1 {
		t.Errorf("cleared %d entries, want 1", removed)
	}
	left := a.History("", "", "", 0)
	if len(left) != 2 || left[0].ConnID != "c2" {
		t.Fatalf("wrong survivors: %#v", left)
	}
	if data, err := os.ReadFile(a.historyPath); err == nil {
		var stored []HistoryEntry
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatalf("history file is not readable: %v", err)
		}
		if len(stored) != 2 {
			t.Errorf("file holds %d entries, want the 2 survivors", len(stored))
		}
	}
	if removed := a.ClearHistory("c1", "zs", "gs_lock"); removed != 0 {
		t.Errorf("second clear removed %d, want 0", removed)
	}
	if removed := a.ClearHistory("", "", ""); removed != 2 {
		t.Errorf("clear-all removed %d, want 2", removed)
	}
	if len(a.History("", "", "", 0)) != 0 {
		t.Error("history survived a clear-all")
	}
}

// AI 面的语句必须在历史里可辨识：MCP 工具调用标记 mcp、批准的助手提案标记
// llm，用户手打的保持无标记（旧历史文件无该字段，向后兼容）。
func TestHistorySourceAttribution(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	histPath := filepath.Join(t.TempDir(), "history.json")
	app.historyPath = histPath
	t.Cleanup(func() { _ = app.Disconnect("c1") })

	if r := app.ExecuteQuery("c1", "SELECT id FROM t", ""); r.Error != "" {
		t.Fatalf("user query: %s", r.Error)
	}
	if r := app.ExecuteQueryAs(HistorySourceMCP, "c1", "SELECT id FROM t", ""); r.Error != "" {
		t.Fatalf("mcp query: %s", r.Error)
	}
	if r := app.ExecuteQueryAs(HistorySourceLLM, "c1", "SELECT id FROM t", ""); r.Error != "" {
		t.Fatalf("llm query: %s", r.Error)
	}

	entries := app.History("c1", "", "", 100)
	// The fixture's CREATE/INSERT run unmarked too; the assertion matches the
	// shared "SELECT id" statement, of which there is one per source.
	sources := map[string]int{}
	for _, e := range entries {
		if !strings.Contains(e.SQL, "SELECT id FROM t") {
			if e.Source != HistorySourceUser {
				t.Fatalf("a setup statement must stay unmarked: %+v", e)
			}
			continue
		}
		sources[e.Source]++
	}
	if sources[HistorySourceUser] != 1 || sources[HistorySourceMCP] != 1 || sources[HistorySourceLLM] != 1 {
		t.Fatalf("sources = %v, want one of each", sources)
	}

	// 来源字段随文件往返（Fyne 客户端共享历史文件）。
	app.writeHistory()
	app2 := &App{historyPath: histPath}
	app2.loadHistory()
	for _, e := range app2.history {
		if e.Source == HistorySourceMCP {
			return
		}
	}
	t.Fatal("the mcp source did not survive the file round trip")
}
