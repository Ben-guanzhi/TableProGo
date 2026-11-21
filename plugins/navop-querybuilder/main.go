// Command navop-querybuilder — 查询构建器（Navicat View Builder 等价物）——声明式 SelectSpec → 方言 SQL（内核 querybuilder 直驱）
//
// 插件协议（plugindriver）：banner + newline-JSON；宿主零改动——
// 与 navop-* 同构，声明 ConnAction 清单、执行 RunAction。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
	"github.com/Ben-guanzhi/dbcore/sqltext"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto: plugindriver.ProtocolName, Version: plugindriver.ProtocolVersion,
		Kind: "navop-querybuilder", Name: "navop-querybuilder",
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
				ID: "build", Title: "构建 SELECT 语句", Group: "SQL 工具",
				Params: []plugindriver.ConnActionParam{
					{Name: "table", Label: "表", Kind: "text", Required: true, Default: ""},
					{Name: "columns", Label: "列（逗号分隔）", Kind: "text", Required: false, Default: "*"},
					{Name: "where", Label: "WHERE（可选）", Kind: "text", Required: false, Default: ""},
					{Name: "order", Label: "ORDER BY（可选）", Kind: "text", Required: false, Default: ""},
					{Name: "limit", Label: "LIMIT（可选）", Kind: "number", Required: false, Default: ""},
				},
			},
		})}
	case plugindriver.MethodConnRunAction:
		var p plugindriver.ConnRunActionParams
		if err := json.Unmarshal(mustJSON(req.Params), &p); err != nil {
			return plugindriver.Response{Error: err.Error()}
		}
		switch p.Action {
		case "build":
			table, _ := p.Args["table"].(string)
			if table == "" {
				return plugindriver.Response{Error: "缺少 table"}
			}
			cols, _ := p.Args["columns"].(string)
			if cols == "" {
				cols = "*"
			}
			var fields []sqltext.QBColumn
			for _, c := range strings.Split(cols, ",") {
				c = strings.TrimSpace(c)
				if c != "" {
					fields = append(fields, sqltext.QBColumn{Col: c})
				}
			}
			spec := sqltext.SelectSpec{Table: table, Columns: fields}
			if s, _ := p.Args["where"].(string); s != "" {
				spec.Where = []sqltext.QBCond{{Kind: "raw", Raw: s}}
			}
			if s, _ := p.Args["order"].(string); s != "" {
				spec.OrderBy = []sqltext.QBOrder{{Col: s}}
			}
			if n, ok := p.Args["limit"].(float64); ok && n > 0 {
				spec.Limit = int(n)
			}
			sql := sqltext.BuildSelect(spec)
			return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{
				Columns: []plugindriver.ConnColumn{{Name: "sql"}},
				Rows:    [][]any{{sql}},
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
