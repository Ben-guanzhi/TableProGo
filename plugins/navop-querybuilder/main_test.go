package main

// 查询构建器插件的 action 验证（§391）：ListActions 清单 + 构建 SELECT。
import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
)

func TestListActions(t *testing.T) {
	resp := handle(plugindriver.Request{Method: plugindriver.MethodConnListActions})
	var actions []plugindriver.ConnAction
	if err := json.Unmarshal(resp.Result, &actions); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].ID != "build" {
		t.Fatalf("actions = %+v", actions)
	}
}

func TestBuildMultiTableJoin(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "build",
			"args": map[string]any{
				"table": "orders o", "columns": "o.id, u.name, COUNT:o.id",
				"joins": `[{"table":"users","alias":"u","type":"LEFT","on":"o.user_id = u.id"}]`,
				"where": "o.total > 100", "groupby": "u.name", "limit": float64(10),
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
	sql := res.Rows[0][0].(string)
	for _, want := range []string{"LEFT JOIN", "users", "u.name", "COUNT", "o.user_id = u.id", "GROUP BY", "LIMIT 10"} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql = %q, missing %q", sql, want)
		}
	}
}

func TestBuildBadJoins(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "build",
			"args":   map[string]any{"table": "t", "joins": "not-json"},
		}),
	})
	if resp.Error == "" {
		t.Fatal("bad joins must error")
	}
}

func TestBuildSelect(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "build",
			"args":   map[string]any{"table": "users", "columns": "id, name", "where": "active = 1", "order": "id", "limit": float64(5)},
		}),
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var res plugindriver.ConnQueryResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	sql := res.Rows[0][0].(string)
	for _, want := range []string{"SELECT", "users", "id", "name", "active = 1", "LIMIT 5"} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql = %q, missing %q", sql, want)
		}
	}
}
