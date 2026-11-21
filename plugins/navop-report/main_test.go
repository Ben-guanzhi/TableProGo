package main

// 报表设计器插件的 action 验证（§391）。
import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
	"github.com/Ben-guanzhi/dbcore/sqlite"
)

func TestReportFromDataSQLite(t *testing.T) {
	db := filepath.Join(t.TempDir(), "r.db")
	// 用内核直接建表——插件只负责查询与渲染。
	ctx := context.Background()
	drv, err := sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drv.Execute(ctx, `CREATE TABLE goods (id INTEGER, label TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := drv.Execute(ctx, `INSERT INTO goods VALUES (1, 'widget'), (2, 'gadget')`); err != nil {
		t.Fatal(err)
	}
	drv.Close()

	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "fromdata",
			"args": map[string]any{
				"title": "库存报表", "sql": "SELECT * FROM goods",
				"kind": "sqlite", "database": db,
			},
		}),
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var res plugindriver.ConnQueryResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	html := res.Rows[0][0].(string)
	md := res.Rows[0][1].(string)
	for _, want := range []string{"库存报表", "widget", "gadget", "<table>", "2 rows"} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q", want)
		}
	}
	if !strings.Contains(md, "widget") || !strings.Contains(md, "共 2 行") {
		t.Errorf("markdown = %s", md)
	}
}

func TestReportHTMLThrowsEscape(t *testing.T) {
	out := renderHTML("x", []string{"c"}, [][]string{{"<script>alert(1)</script>"}})
	if strings.Contains(out, "<script>") {
		t.Fatal("HTML must escape cell values")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatal("escaped form missing")
	}
}

func TestReportDesign(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "design",
			"args": map[string]any{
				"title":   "销售报表",
				"columns": `["month","total"]`,
				"rows":    `[{"month":"01","total":120},{"month":"02","total":240}]`,
			},
		}),
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var res plugindriver.ConnQueryResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	report := res.Rows[0][0].(string)
	for _, want := range []string{"销售报表", "month", "total", "120", "共 2 行"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q", want)
		}
	}
}

func TestReportBadColumns(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "design",
			"args":   map[string]any{"title": "x", "columns": "not-json", "rows": "[]"},
		}),
	})
	if resp.Error == "" {
		t.Fatal("bad columns must error")
	}
}
