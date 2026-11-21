// Command navop-report — 报表设计器（Navicat Report Designer 等价物）——声明式报表（标题/列/分组/合计）→ Markdown 报表
//
// 插件协议（plugindriver）：banner + newline-JSON；宿主零改动——
// 与 navop-* 同构，声明 ConnAction 清单、执行 RunAction。

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Ben-guanzhi/dbcore"
	_ "github.com/Ben-guanzhi/dbcore/dm"
	_ "github.com/Ben-guanzhi/dbcore/mssql"
	_ "github.com/Ben-guanzhi/dbcore/mysql"
	_ "github.com/Ben-guanzhi/dbcore/oracle"
	"github.com/Ben-guanzhi/dbcore/plugindriver"
	_ "github.com/Ben-guanzhi/dbcore/postgres"
	_ "github.com/Ben-guanzhi/dbcore/sqlite"
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
				ID: "design", Title: "生成报表（手填数据）", Group: "报表",
				Params: []plugindriver.ConnActionParam{
					{Name: "title", Label: "报表标题", Kind: "text", Required: true, Default: ""},
					{Name: "columns", Label: "数据列（JSON 数组）", Kind: "textarea", Required: true, Default: ""},
					{Name: "rows", Label: "数据行（JSON 数组对象）", Kind: "textarea", Required: true, Default: ""},
				},
			},
			{
				ID: "fromdata", Title: "从数据库生成报表（SQL → HTML）", Group: "报表",
				Params: []plugindriver.ConnActionParam{
					{Name: "title", Label: "报表标题", Required: true, Default: ""},
					{Name: "sql", Label: "查询 SQL", Kind: "textarea", Required: true, Rows: 5, Default: "SELECT * FROM t"},
					{Name: "kind", Label: "引擎", Required: true, Default: "mysql"},
					{Name: "host", Label: "主机"}, {Name: "port", Label: "端口", Kind: "number"},
					{Name: "user", Label: "用户"}, {Name: "password", Label: "密码"},
					{Name: "database", Label: "数据库"},
				},
			},
		})}
	case plugindriver.MethodConnRunAction:
		var p plugindriver.ConnRunActionParams
		if err := json.Unmarshal(mustJSON(req.Params), &p); err != nil {
			return plugindriver.Response{Error: err.Error()}
		}
		switch p.Action {
		case "fromdata":
			return reportFromData(p.Args)
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

// reportFromData 现场拨号执行 SQL → 内核 QueryResult → HTML + Markdown
// 双格式报表。值经内核 Value.String() 渲染（不做自定义类型假设）。
func reportFromData(args map[string]any) plugindriver.Response {
	title, _ := args["title"].(string)
	query, _ := args["sql"].(string)
	kind, _ := args["kind"].(string)
	if title == "" || query == "" || kind == "" {
		return plugindriver.Response{Error: "缺少 title / sql / kind"}
	}
	port := 0
	if n, ok := args["port"].(float64); ok && n > 0 {
		port = int(n)
	}
	str := func(k string) string { s, _ := args[k].(string); return s }
	spec := dbcore.ConnSpec{
		Kind: dbcore.DatabaseKind(kind), Host: str("host"), Port: port,
		User: str("user"), Password: str("password"), Database: str("database"),
	}
	if spec.Kind == dbcore.KindSQLite {
		spec.File = spec.Database
		spec.Database = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	drv, err := dbcore.Open(ctx, spec, dbcore.PoolConfig{MaxOpen: 1})
	if err != nil {
		return plugindriver.Response{Error: "拨号失败: " + err.Error()}
	}
	defer drv.Close()

	sqldrv, ok := drv.AsSQL()
	if !ok {
		return plugindriver.Response{Error: "该引擎无 SQL 面"}
	}
	res, err := sqldrv.Execute(ctx, query)
	if err != nil {
		return plugindriver.Response{Error: "查询失败: " + err.Error()}
	}
	names := make([]string, 0, len(res.Columns))
	for _, c := range res.Columns {
		names = append(names, c.Name)
	}
	// 文本值矩阵（内核 Value.String() 是规范呈现）。
	vals := make([][]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		r := make([]string, 0, len(row))
		for _, v := range row {
			r = append(r, v.String())
		}
		vals = append(vals, r)
	}
	html := renderHTML(title, names, vals)
	md := renderReport(title, names, rowMaps(names, vals))
	return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{
		Columns: []plugindriver.ConnColumn{{Name: "html"}, {Name: "markdown"}, {Name: "rows"}},
		Rows:    [][]any{{html, md, len(vals)}},
	})}
}

func rowMaps(cols []string, vals [][]string) []map[string]any {
	out := make([]map[string]any, 0, len(vals))
	for _, r := range vals {
		m := map[string]any{}
		for i, c := range cols {
			if i < len(r) {
				m[c] = r[i]
			}
		}
		out = append(out, m)
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
