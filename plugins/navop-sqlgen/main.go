// Command navop-sqlgen is the reference declarative-action plugin. It proves
// the heavy-product-feature-via-plugin path end to end without owning any
// host UI: it declares a composite form (textarea + select + checkbox) through
// ConnAction's additive protocol, and the host renders that form with native
// controls and sends the values back to RunAction.
//
// The action here is deliberately small (SQL formatting) — the point is the
// PROTOCOL SHAPE, not the feature. PK/FK designers and result-to-table wizards
// plug into the same ListActions/RunAction contract with richer params.
//
// It is an ordinary newline-JSON plugin: banner on stdout, one Request per
// line on stdin, one Response per line out. No extra dependency beyond dbcore.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
	"github.com/Ben-guanzhi/dbcore/sqlformat"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    "navop-sqlgen",
		Name:    "SQL 生成插件（声明式动作参考实现）",
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
		return plugindriver.Response{Result: mustJSON([]plugindriver.ConnAction{{
			ID: "format", Title: "格式化 SQL", Group: "SQL 工具",
			Params: []plugindriver.ConnActionParam{
				{
					Name: "sql", Label: "SQL 文本", Kind: "textarea",
					Required: true, Rows: 8, Help: "要格式化的 SQL",
				},
				{
					Name: "style", Label: "输出风格", Kind: "select",
					Options: []string{"compact", "expanded"}, Default: "expanded",
				},
				{
					Name: "upper", Label: "关键字大写", Kind: "checkbox",
					Default: "true",
				},
			},
		}})}
	case plugindriver.MethodConnRunAction:
		var p plugindriver.ConnRunActionParams
		if err := json.Unmarshal(mustJSON(req.Params), &p); err != nil {
			return plugindriver.Response{Error: err.Error()}
		}
		if p.Action != "format" {
			return plugindriver.Response{Error: "unknown action: " + p.Action}
		}
		sql, _ := p.Args["sql"].(string)
		formatted := sqlformat.Format(sql)
		return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{
			Columns: []plugindriver.ConnColumn{{Name: "formatted_sql"}},
			Rows:    [][]any{{formatted}},
		})}
	}
	return plugindriver.Response{Error: "unsupported method: " + req.Method}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
