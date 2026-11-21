package main

// 协作评论插件的持久化验证（§391）：add → list（文件隔离）。
import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
)

func TestCollabAddList(t *testing.T) {
	t.Setenv("NAVOP_COLLAB_STORE", filepath.Join(t.TempDir(), "comments.jsonl"))
	args := map[string]any{"sql": "SELECT * FROM t WHERE a = 1", "comment": "检查索引", "author": "alice"}
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{"action": "add", "args": args}),
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	resp2 := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{"action": "list", "args": map[string]any{"sql": args["sql"]}}),
	})
	if resp2.Error != "" {
		t.Fatal(resp2.Error)
	}
	var res plugindriver.ConnQueryResult
	if err := json.Unmarshal(resp2.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0][1] != "alice" || res.Rows[0][2] != "检查索引" {
		t.Fatalf("rows = %+v", res.Rows)
	}
}
