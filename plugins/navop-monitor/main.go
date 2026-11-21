// Command navop-monitor — 监控面板（DBeaver 等价物）——采集服务器指标 + 阈值判定，按状态着色
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
	"strconv"
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
				ID: "check", Title: "检查指标阈值（手填数据）", Group: "监控",
				Params: []plugindriver.ConnActionParam{
					{Name: "metrics", Label: "指标（JSON 数组：name/value/threshold）", Kind: "textarea", Required: true, Default: ""},
				},
			},
			{
				ID: "collect", Title: "采集服务器实时指标", Group: "监控",
				Params: []plugindriver.ConnActionParam{
					{Name: "kind", Label: "引擎", Required: true, Default: "mysql"},
					{Name: "host", Label: "主机"}, {Name: "port", Label: "端口", Kind: "number"},
					{Name: "user", Label: "用户"}, {Name: "password", Label: "密码"},
					{Name: "database", Label: "数据库"},
					{Name: "alerts", Label: "告警阈值（JSON：label=上限）", Kind: "textarea", Default: "{}"},
				},
			},
		})}
	case plugindriver.MethodConnRunAction:
		var p plugindriver.ConnRunActionParams
		if err := json.Unmarshal(mustJSON(req.Params), &p); err != nil {
			return plugindriver.Response{Error: err.Error()}
		}
		switch p.Action {
		case "collect":
			return collectMetrics(p.Args)
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

// collectMetrics 现场拨号 → 内核 AsMetricsSource 真实采集（各引擎的
// WideSession 已内置精选指标：MySQL SHOW GLOBAL STATUS 等）→ 可选阈值告警。
func collectMetrics(args map[string]any) plugindriver.Response {
	kind, _ := args["kind"].(string)
	if kind == "" {
		return plugindriver.Response{Error: "缺少 kind"}
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	drv, err := dbcore.Open(ctx, spec, dbcore.PoolConfig{MaxOpen: 1})
	if err != nil {
		return plugindriver.Response{Error: "拨号失败: " + err.Error()}
	}
	defer drv.Close()

	src, ok := dbcore.AsMetricsSource(drv)
	if !ok {
		return plugindriver.Response{Error: "该引擎未提供指标面（MetricsSource 未实现）——SQLite 等无服务器指标"}
	}
	metrics, err := src.ServerMetrics(ctx)
	if err != nil {
		return plugindriver.Response{Error: "采集失败: " + err.Error()}
	}

	// 可选告警阈值（label → 数值上限）。
	thresholds := map[string]float64{}
	if raw, _ := args["alerts"].(string); strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &thresholds); err != nil {
			return plugindriver.Response{Error: "alerts 必须是 JSON 对象: " + err.Error()}
		}
	}
	cols := []plugindriver.ConnColumn{{Name: "metric"}, {Name: "value"}, {Name: "group"}, {Name: "status"}}
	var rows [][]any
	for _, m := range metrics {
		status := "OK"
		if limit, ok := thresholds[m.Label]; ok {
			if n, err := strconv.ParseFloat(strings.ReplaceAll(m.Value, ",", ""), 64); err == nil && n > limit {
				status = "ALERT"
			}
		}
		rows = append(rows, []any{m.Label, m.Value, m.Group, status})
	}
	return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{Columns: cols, Rows: rows})}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
