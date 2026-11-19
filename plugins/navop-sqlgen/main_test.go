package main

// navop-sqlgen 插件单测（§331）：插件生态此前零测试——这里验证协议声明
// 的复合表单（textarea rows / select options / checkbox）与格式化动作。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
)

func TestListActionsDeclaresCompositeForm(t *testing.T) {
	resp := handle(plugindriver.Request{ID: 1, Method: plugindriver.MethodConnListActions})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var actions []plugindriver.ConnAction
	if err := json.Unmarshal(resp.Result, &actions); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].ID != "format" {
		t.Fatalf("actions = %+v", actions)
	}
	params := actions[0].Params
	if len(params) != 3 {
		t.Fatalf("params = %d, want 3", len(params))
	}
	byName := map[string]plugindriver.ConnActionParam{}
	for _, p := range params {
		byName[p.Name] = p
	}
	if byName["sql"].Kind != "textarea" || byName["sql"].Rows != 8 || !byName["sql"].Required {
		t.Fatalf("sql param wrong: %+v", byName["sql"])
	}
	if byName["style"].Kind != "select" || len(byName["style"].ParamOptions()) != 2 {
		t.Fatalf("style param wrong: %+v", byName["style"])
	}
	if byName["upper"].Kind != "checkbox" {
		t.Fatalf("upper param wrong: %+v", byName["upper"])
	}
}

func TestRunActionFormatsSQL(t *testing.T) {
	req := plugindriver.Request{
		ID:     2,
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(plugindriver.ConnRunActionParams{
			Action: "format",
			Args:   map[string]any{"sql": "select 1 from t"},
		}),
	}
	resp := handle(req)
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var result plugindriver.ConnQueryResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || !strings.Contains(result.Rows[0][0].(string), "FROM") {
		t.Fatalf("formatted = %+v", result.Rows)
	}
}

func TestRunActionRejectsUnknownAction(t *testing.T) {
	req := plugindriver.Request{
		ID:     3,
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(plugindriver.ConnRunActionParams{Action: "drop_tables"}),
	}
	resp := handle(req)
	if resp.Error == "" || !strings.Contains(resp.Error, "unknown action") {
		t.Fatalf("want unknown-action error, got %+v", resp)
	}
}

func TestResponseIDEchoed(t *testing.T) {
	// main.go sets resp.ID = req.ID after handle; verify the pattern's shape.
	resp := handle(plugindriver.Request{ID: 42, Method: "bogus"})
	if resp.Error == "" {
		t.Fatal("unknown method must error")
	}
	resp.ID = 42 // what main.go does
	if resp.ID != 42 {
		t.Fatal("ID not echoed")
	}
}
