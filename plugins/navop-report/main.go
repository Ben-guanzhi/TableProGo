// Command navop-report — 报表设计器（Navicat Report Designer 等价物）——声明式报表（标题/列/分组/合计）→ Markdown 报表
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
		Kind: "navop-report", Name: "navop-report",
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
				ID: "design", Title: "生成报表", Group: "报表",
				Params: []plugindriver.ConnActionParam{
					{Name: "title", Label: "报表标题", Kind: "text", Required: true, Default: ""},
					{Name: "columns", Label: "数据列（JSON 数组）", Kind: "textarea", Required: true, Default: ""},
					{Name: "rows", Label: "数据行（JSON 数组对象）", Kind: "textarea", Required: true, Default: ""},
				},
			},
		})}
	case plugindriver.MethodConnRunAction:
		var p plugindriver.ConnRunActionParams
		if err := json.Unmarshal(mustJSON(req.Params), &p); err != nil {
			return plugindriver.Response{Error: err.Error()}
		}
		switch p.Action {
		case "design":
			title, _ := p.Args["title"].(string)
			colsRaw, _ := p.Args["columns"].(string)
			rowsRaw, _ := p.Args["rows"].(string)
			var cols []string
			if err := json.Unmarshal([]byte(colsRaw), &cols); err != nil {
				return plugindriver.Response{Error: "columns 必须是 JSON 字符串数组: " + err.Error()}
			}
			var rows []map[string]any
			if err := json.Unmarshal([]byte(rowsRaw), &rows); err != nil {
				return plugindriver.Response{Error: "rows 必须是 JSON 对象数组: " + err.Error()}
			}
			if title == "" || len(cols) == 0 {
				return plugindriver.Response{Error: "缺少 title / columns"}
			}
			report := renderReport(title, cols, rows)
			return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{
				Columns: []plugindriver.ConnColumn{{Name: "report"}},
				Rows:    [][]any{{report}},
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
