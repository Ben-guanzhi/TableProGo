// Command navop-querybuilder — 查询构建器（Navicat View Builder 等价物）——声明式 SelectSpec → 方言 SQL（内核 querybuilder 直驱）
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
	"strings"
	"time"

	"github.com/Ben-guanzhi/dbcore"
	_ "github.com/Ben-guanzhi/dbcore/dm"
	_ "github.com/Ben-guanzhi/dbcore/mssql"
	_ "github.com/Ben-guanzhi/dbcore/mysql"
	_ "github.com/Ben-guanzhi/dbcore/oracle"
	"github.com/Ben-guanzhi/dbcore/plugindriver"
	_ "github.com/Ben-guanzhi/dbcore/postgres"
	_ "github.com/Ben-guanzhi/dbcore/sqlite"
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
			{
				ID: "preview", Title: "构建并预览结果", Group: "SQL 工具",
				Params: []plugindriver.ConnActionParam{
					{Name: "table", Label: "主表", Required: true, Default: ""},
					{Name: "columns", Label: "列", Default: "*"},
					{Name: "where", Label: "WHERE", Default: ""},
					{Name: "kind", Label: "引擎（预览连接）", Required: true, Default: "mysql"},
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
		case "build":
			sql, err := buildSQL(p.Args)
			if err != nil {
				return plugindriver.Response{Error: err.Error()}
			}
			return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{
				Columns: []plugindriver.ConnColumn{{Name: "sql"}},
				Rows:    [][]any{{sql}},
			})}
		case "preview":
			return previewSQL(p.Args)
		default:
			return plugindriver.Response{Error: "unknown action: " + p.Action}
		}
	}
	return plugindriver.Response{Error: "unsupported method: " + req.Method}
}

// buildSQL 从线路参数构造 SelectSpec 并生成 SQL（build/preview 共用）。
func buildSQL(args map[string]any) (string, error) {
	table, _ := args["table"].(string)
	if table == "" {
		return "", fmt.Errorf("缺少 table")
	}
	cols, _ := args["columns"].(string)
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
		if agg, col, ok := strings.Cut(c, ":"); ok {
			switch strings.ToUpper(agg) {
			case "COUNT", "SUM", "AVG", "MIN", "MAX":
				field = sqltext.QBColumn{Col: col, Agg: strings.ToUpper(agg)}
			}
		}
		fields = append(fields, field)
	}
	spec := sqltext.SelectSpec{Table: table, Columns: fields}
	spec.Alias, _ = args["alias"].(string)
	spec.Distinct, _ = args["distinct"].(bool)
	if raw, _ := args["joins"].(string); strings.TrimSpace(raw) != "" {
		var js []joinSpec
		if err := json.Unmarshal([]byte(raw), &js); err != nil {
			return "", fmt.Errorf("joins 必须是 JSON 数组: %w", err)
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
	if s, _ := args["where"].(string); s != "" {
		spec.Where = []sqltext.QBCond{{Kind: "raw", Raw: s}}
	}
	if s, _ := args["groupby"].(string); s != "" {
		for _, g := range strings.Split(s, ",") {
			if g = strings.TrimSpace(g); g != "" {
				spec.GroupBy = append(spec.GroupBy, g)
			}
		}
	}
	if s, _ := args["order"].(string); s != "" {
		spec.OrderBy = []sqltext.QBOrder{{Col: s}}
	}
	if n, ok := args["limit"].(float64); ok && n > 0 {
		spec.Limit = int(n)
	}
	return sqltext.BuildSelect(spec), nil
}

// previewSQL 构建后立即执行（单次请求即预览；"输入即自动刷新"需宿主
// 在编辑器加触发按钮——协议本身只做单次）。
func previewSQL(args map[string]any) plugindriver.Response {
	query, err := buildSQL(args)
	if err != nil {
		return plugindriver.Response{Error: err.Error()}
	}
	kind, _ := args["kind"].(string)
	if kind == "" {
		return plugindriver.Response{Error: "preview 需 kind"}
	}
	port := 0
	if n, ok := args["port"].(float64); ok && n > 0 {
		port = int(n)
	}
	str := func(k string) string { s, _ := args[k].(string); return s }
	spec := dbcore.ConnSpec{Kind: dbcore.DatabaseKind(kind), Host: str("host"), Port: port,
		User: str("user"), Password: str("password"), Database: str("database")}
	if spec.Kind == dbcore.KindSQLite {
		spec.File = spec.Database
		spec.Database = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
		return plugindriver.Response{Error: "预览执行失败: " + err.Error()}
	}
	cols := make([]plugindriver.ConnColumn, 0, len(res.Columns))
	for _, c := range res.Columns {
		cols = append(cols, plugindriver.ConnColumn{Name: c.Name})
	}
	var rows [][]any
	for _, r := range res.Rows {
		row := make([]any, 0, len(r))
		for _, v := range r {
			row = append(row, v.String())
		}
		rows = append(rows, row)
	}
	return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{Columns: cols, Rows: rows})}
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
