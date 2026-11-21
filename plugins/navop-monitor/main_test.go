package main

// 监控插件的阈值判定验证（§391）。
import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
)

func TestMonitorHistory(t *testing.T) {
	// 有状态 ring：两次 collect 后 history 应有 2 个时间点。
	for i := 0; i < 2; i++ {
		resp := handle(plugindriver.Request{
			Method: plugindriver.MethodConnRunAction,
			Params: mustJSON(map[string]any{
				"action": "collect",
				"args":   map[string]any{"kind": "sqlite", "database": ":memory:"},
			}),
		})
		if resp.Error != "" {
			t.Fatal(resp.Error)
		}
	}
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{"action": "history", "args": map[string]any{}}),
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var res plugindriver.ConnQueryResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) < 2 {
		t.Fatalf("history rows = %d, want >=2", len(res.Rows))
	}
	if res.Rows[0][0] == nil || res.Rows[0][1] == nil {
		t.Fatal("history row needs at/metric")
	}
}

func TestMonitorCollectSQLite(t *testing.T) {
	// SQLite 驱动经内核 SessionCore 提供基础指标——真拨号采集断言
	// 「有结果、无错误」（不伪造数据）。
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "collect",
			"args":   map[string]any{"kind": "sqlite", "database": ":memory:"},
		}),
	})
	if resp.Error != "" {
		t.Fatalf("collect: %v", resp.Error)
	}
	var res plugindriver.ConnQueryResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) == 0 {
		t.Fatal("collected no metrics")
	}
}

func TestMonitorCollectMissingKind(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{"action": "collect", "args": map[string]any{}}),
	})
	if resp.Error == "" || !strings.Contains(resp.Error, "kind") {
		t.Fatalf("missing kind: %q", resp.Error)
	}
}

func TestMonitorThresholds(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "check",
			"args": map[string]any{
				"metrics": `[{"name":"cpu","value":91,"threshold":80},{"name":"mem","value":45,"threshold":80}]`,
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
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Rows))
	}
	if res.Rows[0][3] != "ALERT" || res.Rows[1][3] != "OK" {
		t.Fatalf("statuses = %v / %v", res.Rows[0][3], res.Rows[1][3])
	}
}
