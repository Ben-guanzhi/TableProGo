package main

// 调试插件的诚实边界验证（§391）：Oracle debug 面前置未满足时返回
// unavailable 而非伪造会话。
import (
	"encoding/json"
	"testing"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
)

func TestDebugHonestUnavailable(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{"action": "sessions", "args": map[string]any{}}),
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var res plugindriver.ConnQueryResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "unavailable" {
		t.Fatalf("rows = %+v", res.Rows)
	}
}
