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
				ID: "build", Title: "构建 SELECT 语句（多表 JOIN）", Group: "SQL 工具",
				Params: []plugindriver.ConnActionParam{
					{Name: "table", Label: "主表", Kind: "text", Required: true, Default: ""},
					{Name: "alias", Label: "主表别名", Kind: "text", Required: false, Default: ""},
					{Name: "columns", Label: "列（逗号分隔；t.col 限定；COUNT:id 聚合）", Kind: "text", Required: false, Default: "*"},
					{Name: "joins", Label: "JOIN（JSON 数组：table/type/on/alias）", Kind: "textarea", Required: false, Rows: 4, Default: ""},
					{Name: "where", Label: "WHERE（可选）", Kind: "text", Required: false, Default: ""},
					{Name: "groupby", Label: "GROUP BY（可选，逗号分隔）", Kind: "text", Required: false, Default: ""},
					{Name: "order", Label: "ORDER BY（可选）", Kind: "text", Required: false, Default: ""},
					{Name: "limit", Label: "LIMIT（可选）", Kind: "number", Required: false, Default: ""},
					{Name: "distinct", Label: "SELECT DISTINCT", Kind: "checkbox", Required: false, Default: "false"},
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
				if c == "" {
					continue
				}
				field := sqltext.QBColumn{Col: c}
				// 聚合简写：COUNT:id / SUM:amount / AVG:score / MIN:... / MAX:...
				if agg, col, ok := strings.Cut(c, ":"); ok {
					switch strings.ToUpper(agg) {
					case "COUNT", "SUM", "AVG", "MIN", "MAX":
						field = sqltext.QBColumn{Col: col, Agg: strings.ToUpper(agg)}
					}
				}
				fields = append(fields, field)
			}
			spec := sqltext.SelectSpec{Table: table, Columns: fields}
			spec.Alias, _ = p.Args["alias"].(string)
			spec.Distinct, _ = p.Args["distinct"].(bool)

			// JOIN：JSON 数组 [{table,type,on,alias}, ...]（内核 QBJoin 直驱）。
			if raw, _ := p.Args["joins"].(string); strings.TrimSpace(raw) != "" {
				var js []joinSpec
				if err := json.Unmarshal([]byte(raw), &js); err != nil {
					return plugindriver.Response{Error: "joins 必须是 JSON 数组: " + err.Error()}
				}
				for _, j := range js {
					if j.Table == "" {
						continue
					}
					join := sqltext.QBJoin{Table: j.Table, Alias: j.Alias, Type: strings.ToUpper(j.Type)}
					if join.Type == "" {
						join.Type = "INNER"
					}
					for _, on := range strings.Split(j.On, ",") {
						if on = strings.TrimSpace(on); on != "" {
							join.On = append(join.On, sqltext.QBCond{Kind: "raw", Raw: on})
						}
					}
					spec.Joins = append(spec.Joins, join)
				}
			}

			if s, _ := p.Args["where"].(string); s != "" {
				spec.Where = []sqltext.QBCond{{Kind: "raw", Raw: s}}
			}
			if s, _ := p.Args["groupby"].(string); s != "" {
				for _, g := range strings.Split(s, ",") {
					if g = strings.TrimSpace(g); g != "" {
						spec.GroupBy = append(spec.GroupBy, g)
					}
				}
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

// joinSpec 是插件线路层的 JOIN 描述（映射内核 sqltext.QBJoin）。
type joinSpec struct {
	Table string `json:"table"`
	Alias string `json:"alias,omitempty"`
	Type  string `json:"type,omitempty"` // INNER | LEFT | RIGHT | CROSS
	On    string `json:"on,omitempty"`   // 逗号分隔的 ON 条件
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
