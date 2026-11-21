// Command navop-debug — PL/SQL 调试器协议面（前置骨架）——声明式断点注册/会话状态；Oracle 插件 debug 面就绪前诚实返回不支持
//
// 插件协议（plugindriver）：banner + newline-JSON；宿主零改动——
// 与 navop-* 同构，声明 ConnAction 清单、执行 RunAction。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto: plugindriver.ProtocolName, Version: plugindriver.ProtocolVersion,
		Kind: "navop-debug", Name: "navop-debug",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var req plugindriver.Request
			if json.Unmarshal(line, &req) == nil {
				resp := handle(req)
				resp.ID = req.ID
				b, _ := json.Marshal(resp)
				fmt.Fprintf(out, "%s\n", b)
				out.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func handle(req plugindriver.Request) plugindriver.Response {
	switch req.Method {
	case plugindriver.MethodConnListActions:
		return plugindriver.Response{Result: mustJSON([]plugindriver.ConnAction{
			{
				ID: "sessions", Title: "调试会话状态", Group: "调试",
				Params: []plugindriver.ConnActionParam{},
			},
		})}
	case plugindriver.MethodConnRunAction:
		var p plugindriver.ConnRunActionParams
		if err := json.Unmarshal(mustJSON(req.Params), &p); err != nil {
			return plugindriver.Response{Error: err.Error()}
		}
		switch p.Action {
		case "sessions":
			// Oracle 插件的 DBMS_DEBUG_JDWP 面尚未实现——插件声明能力边界，
			// 宿主显示明确状态而非神秘失败（§19697 超范围裁定的协议骨架）。
			return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{
				Columns: []plugindriver.ConnColumn{{Name: "status"}, {Name: "detail"}},
				Rows:    [][]any{{"unavailable", "Oracle debug 面前置未满足：需插件实现 DBMS_DEBUG_JDWP 协议"}},
			})}
		default:
			return plugindriver.Response{Error: "unknown action: " + p.Action}
		}
	}
	return plugindriver.Response{Error: "unsupported method: " + req.Method}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
