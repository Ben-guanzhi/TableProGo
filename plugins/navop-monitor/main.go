// Command navop-monitor — 监控面板（DBeaver 等价物）——采集服务器指标 + 阈值判定，按状态着色
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
		Kind: "navop-monitor", Name: "navop-monitor",
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
				ID: "check", Title: "检查指标阈值", Group: "监控",
				Params: []plugindriver.ConnActionParam{
					{Name: "metrics", Label: "指标（JSON 数组：name/value/threshold）", Kind: "textarea", Required: true, Default: ""},
				},
			},
		})}
	case plugindriver.MethodConnRunAction:
		var p plugindriver.ConnRunActionParams
		if err := json.Unmarshal(mustJSON(req.Params), &p); err != nil {
			return plugindriver.Response{Error: err.Error()}
		}
		switch p.Action {
		case "check":
			raw, _ := p.Args["metrics"].(string)
			var ms []metricIn
			if err := json.Unmarshal([]byte(raw), &ms); err != nil {
				return plugindriver.Response{Error: "metrics 必须是 JSON 数组: " + err.Error()}
			}
			cols := []plugindriver.ConnColumn{{Name: "metric"}, {Name: "value"}, {Name: "threshold"}, {Name: "status"}}
			var rows [][]any
			for _, m := range ms {
				status := "OK"
				if m.Value > m.Threshold {
					status = "ALERT"
				}
				rows = append(rows, []any{m.Name, m.Value, m.Threshold, status})
			}
			return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{Columns: cols, Rows: rows})}
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
