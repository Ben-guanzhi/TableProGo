package main

// 查询构建器插件的 action 验证（§391）：ListActions 清单 + 构建 SELECT。
import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
	"github.com/Ben-guanzhi/dbcore/sqlite"
)

func TestPreviewSQLite(t *testing.T) {
	// 真库 → 构建 + 预览（单次执行）→ 网格结果。
	db := filepath.Join(t.TempDir(), "q.db")
	ctx := context.Background()
	drv, err := sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drv.Execute(ctx, `CREATE TABLE goods (id INTEGER, label TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := drv.Execute(ctx, `INSERT INTO goods VALUES (1, 'a'), (2, 'b')`); err != nil {
		t.Fatal(err)
	}
	drv.Close()

	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "preview",
			"args": map[string]any{
				"table": "goods", "columns": "id, label", "kind": "sqlite", "database": db,
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
	if len(res.Rows) != 2 || len(res.Columns) != 2 {
		t.Fatalf("preview rows=%d cols=%d, want 2/2", len(res.Rows), len(res.Columns))
	}
	if res.Rows[0][1] != "a" {
		t.Fatalf("row = %v, want label a", res.Rows[0])
	}
}

func TestPreviewMissingKind(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "preview", "args": map[string]any{"table": "t"},
		}),
	})
	if resp.Error == "" || !strings.Contains(resp.Error, "kind") {
		t.Fatalf("missing kind: %q", resp.Error)
	}
}

func TestListActions(t *testing.T) {
	resp := handle(plugindriver.Request{Method: plugindriver.MethodConnListActions})
	var actions []plugindriver.ConnAction
	if err := json.Unmarshal(resp.Result, &actions); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0].ID != "build" || actions[1].ID != "preview" {
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
