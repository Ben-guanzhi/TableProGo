// Command navop-collab — 协作评论（DataGrip 等价物）——按语句哈希归因的本地评论存储（文件持久化）
//
// 插件协议（plugindriver）：banner + newline-JSON；宿主零改动——
// 与 navop-* 同构，声明 ConnAction 清单、执行 RunAction。

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto: plugindriver.ProtocolName, Version: plugindriver.ProtocolVersion,
		Kind: "navop-collab", Name: "navop-collab",
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
				ID: "add", Title: "添加评论", Group: "协作",
				Params: []plugindriver.ConnActionParam{
					{Name: "sql", Label: "SQL 语句", Kind: "textarea", Required: true, Default: ""},
					{Name: "comment", Label: "评论", Kind: "textarea", Required: true, Default: ""},
					{Name: "author", Label: "作者", Kind: "text", Required: false, Default: "local"},
				},
			},
			{
				ID: "list", Title: "列出评论", Group: "协作",
				Params: []plugindriver.ConnActionParam{
					{Name: "sql", Label: "SQL 语句（空=全部）", Kind: "textarea", Required: false, Default: ""},
				},
			},
		})}
	case plugindriver.MethodConnRunAction:
		var p plugindriver.ConnRunActionParams
		if err := json.Unmarshal(mustJSON(req.Params), &p); err != nil {
			return plugindriver.Response{Error: err.Error()}
		}
		switch p.Action {
		case "add":
			sqlText, _ := p.Args["sql"].(string)
			comment, _ := p.Args["comment"].(string)
			author, _ := p.Args["author"].(string)
			if sqlText == "" || comment == "" {
				return plugindriver.Response{Error: "缺少 sql / comment"}
			}
			if author == "" {
				author = "local"
			}
			store, err := commentStorePath()
			if err != nil {
				return plugindriver.Response{Error: err.Error()}
			}
			h := hashSQL(sqlText)
			list, _ := loadComments(store)
			list = append(list, commentEntry{Hash: h, Author: author, Text: comment, At: time.Now().UTC().Format(time.RFC3339)})
			if err := saveComments(store, list); err != nil {
				return plugindriver.Response{Error: err.Error()}
			}
			return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{
				Columns: []plugindriver.ConnColumn{{Name: "hash"}, {Name: "comments"}},
				Rows:    [][]any{{h, len(list)}},
			})}
		case "list":
			store, err := commentStorePath()
			if err != nil {
				return plugindriver.Response{Error: err.Error()}
			}
			list, err := loadComments(store)
			if err != nil {
				return plugindriver.Response{Error: err.Error()}
			}
			if sqlText, _ := p.Args["sql"].(string); sqlText != "" {
				h := hashSQL(sqlText)
				var filtered []commentEntry
				for _, c := range list {
					if c.Hash == h {
						filtered = append(filtered, c)
					}
				}
				list = filtered
			}
			cols := []plugindriver.ConnColumn{{Name: "hash"}, {Name: "author"}, {Name: "comment"}, {Name: "at"}}
			var rows [][]any
			for _, c := range list {
				rows = append(rows, []any{c.Hash, c.Author, c.Text, c.At})
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
