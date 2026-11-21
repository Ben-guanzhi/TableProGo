package main

// 报表设计器插件的 action 验证（§391）。
import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
)

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
