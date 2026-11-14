package main

// SQLucid 三件套（Q-01/Q-02，波 92 §6 处方 2）的后端半：
//
//   Q-01 F5 视觉对应 —— proposal 卡上的 SQL 已有；这里补"分句解释"：把模型给的
//        SQL 按子句切步（SELECT 列 / FROM 表 / JOIN / WHERE / …），每步一段原文
//        + 它引用的列名清单。前端 hover 解释步时高亮结果网格里同名的列。
//
//   Q-02 F6 修复环 —— "可编辑解释→重生成"：refineProposal 把用户的修正说明与
//        原 SQL/原解释一起发给模型，产出的新语句替换原 proposal（ID 不变、Risk
//        重算）。用户改的是"意图描述"，不是 SQL 本身——这正是 SQLucid 的
//        48%→85% 机制（约 1.8 次编辑收敛）。

import (
	"context"
	"fmt"
	"strings"

	"tablepro-go/internal/llm"
	"tablepro-go/internal/sqlrisk"
)

// sqlStep 是 SQL 的一个子句步：原文 + 它引用的标识符（供前端 hover 高亮）。
type sqlStep struct {
	// Clause 是子句名（SELECT / FROM / JOIN / WHERE / …）。
	Clause string `json:"clause"`
	// Text 是该子句的原文（含关键字）。
	Text string `json:"text"`
	// Columns 是该子句引用的列名（去引号后的小写），供 hover 高亮对应结果列。
	Columns []string `json:"columns"`
}

// clauseOrder 是 SQL 文本的子句切分锚点（大小写不敏感匹配）。
var clauseOrder = []string{"SELECT", "FROM", "JOIN", "WHERE", "GROUP BY", "HAVING", "ORDER BY", "LIMIT", "OFFSET"}

// splitSQLSteps 把一条 SQL 按子句切成步。它不做语义分析——静态、纯文本、
// 无数据库往返，所以 proposal 卡可以同步携带（与 Risk 同级）。
func splitSQLSteps(sql string) []sqlStep {
	normalized := strings.TrimSpace(sql)
	if normalized == "" {
		return nil
	}
	type anchor struct {
		name string
		pos  int
	}
	var anchors []anchor
	for _, kw := range clauseOrder {
		pos := findClauseKeyword(normalized, kw)
		if pos >= 0 {
			anchors = append(anchors, anchor{name: kw, pos: pos})
		}
	}
	if len(anchors) == 0 {
		return []sqlStep{{Clause: "QUERY", Text: normalized, Columns: nil}}
	}
	// 按位置排序，取每段 [锚点, 下一锚点)。
	for i := 0; i < len(anchors); i++ {
		for j := i + 1; j < len(anchors); j++ {
			if anchors[j].pos < anchors[i].pos {
				anchors[i], anchors[j] = anchors[j], anchors[i]
			}
		}
	}
	steps := make([]sqlStep, 0, len(anchors))
	for i, a := range anchors {
		end := len(normalized)
		if i+1 < len(anchors) {
			end = anchors[i+1].pos
		}
		text := strings.TrimSpace(normalized[a.pos:end])
		if text == "" {
			continue
		}
		steps = append(steps, sqlStep{
			Clause:  a.name,
			Text:    text,
			Columns: columnsIn(text),
		})
	}
	return steps
}

// findClauseKeyword 在词边界找关键词（大小写不敏感），跳过单引号字符串字面量。
func findClauseKeyword(sql, kw string) int {
	lower := strings.ToLower(sql)
	target := strings.ToLower(kw)
	inString := false
	for i := 0; i+len(target) <= len(lower); i++ {
		switch lower[i] {
		case '\'':
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if lower[i:i+len(target)] != target {
			continue
		}
		// 词边界：前一个与后一个都不是标识符字符。
		before := byte(' ')
		if i > 0 {
			before = lower[i-1]
		}
		after := byte(' ')
		if i+len(target) < len(lower) {
			after = lower[i+len(target)]
		}
		if !isIdentByteDB(before) && !isIdentByteDB(after) {
			return i
		}
	}
	return -1
}

func isIdentByteDB(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// columnsIn 从子句文本里提取可能的列名：按非标识符字符切分、去 SQL 关键字与
// 函数名，保留 2–30 字符的词。启发式，但 hover 高亮只需要"指对方向"。
func columnsIn(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		lw := strings.ToLower(word)
		if lw == "" || len(lw) < 2 || len(lw) > 30 || seen[lw] {
			continue
		}
		switch lw {
		case "select", "from", "where", "and", "or", "not", "in", "like", "is", "null",
			"join", "left", "right", "inner", "outer", "on", "group", "by", "order",
			"having", "limit", "offset", "as", "distinct", "count", "sum", "avg",
			"min", "max", "case", "when", "then", "else", "end", "asc", "desc":
			continue
		}
		seen[lw] = true
		out = append(out, lw)
	}
	return out
}

// refineProposal 应用用户的修正说明：把「原 SQL + 模型原解释 + 用户的修正」作为
// 一条新问题发给模型，要求重写语句。新语句替换原 proposal（ID 不变、Risk 重算、
// Reason 更新为修正说明），等待用户再次裁决——闭环就是 SQLucid 的修复环。
//
// 复用 AskLLM 的配置与历史（模型看得见自己先前的回答），但**不会自动执行**新
// 语句——它只替换等待中的 proposal，执行权始终在用户。
func (a *App) refineProposal(ctx context.Context, id, feedback string) (Proposal, error) {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return Proposal{}, fmt.Errorf("修正说明为空")
	}
	p, ok := proposals.get(id)
	if !ok {
		return Proposal{}, fmt.Errorf("this proposal is no longer waiting (%s)", id)
	}
	question := fmt.Sprintf(
		"你先前提议了这条 SQL：\n%s\n\n意图说明：%s\n\n它不对或不是用户想要的。用户的修正说明：\n%s\n\n请只输出改写后的完整 SQL 本身：不要解释，不要 markdown，不要代码围栏。",
		p.SQL, p.Reason, feedback,
	)
	rewritten, err := a.AskLLMForRefine(ctx, question)
	if err != nil {
		return Proposal{}, err
	}
	rewritten = strings.TrimSpace(rewritten)
	// 剥掉模型偶尔加的 markdown 围栏。
	rewritten = strings.TrimPrefix(rewritten, "```sql")
	rewritten = strings.TrimPrefix(rewritten, "```")
	rewritten = strings.TrimSuffix(rewritten, "```")
	rewritten = strings.TrimSpace(rewritten)
	if rewritten == "" {
		return Proposal{}, fmt.Errorf("模型返回了空 SQL")
	}
	// 替换等待中的 proposal：ID 不变（前端卡片不跳），Risk 重算，Reason 更新为
	// 用户的修正说明（卡上读到的"意图"永远是最新的）。
	replaced, ok := proposals.replace(Proposal{
		ID:         id,
		Connection: p.Connection,
		Database:   p.Database,
		SQL:        rewritten,
		Reason:     feedback,
		Risk:       sqlrisk.Assess(rewritten),
	})
	if !ok {
		return Proposal{}, fmt.Errorf("this proposal is no longer waiting (%s)", id)
	}
	return replaced, nil
}

// AskLLMForRefine 是修复环专用的模型调用：与 AskLLM 同配置同历史，但不把修复
// 轮写进共享对话历史（修复环是 proposal 卡的局部对话，混进主历史会污染后续问答）。
func (a *App) AskLLMForRefine(ctx context.Context, question string) (string, error) {
	cfg := LoadLLMConfig()
	client := llm.New(cfg)
	llmState.mu.Lock()
	history := make([]llm.Message, len(llmState.history))
	copy(history, llmState.history)
	llmState.mu.Unlock()
	// 带上目录工具，让模型可以查结构来自我校正；与 AskLLM 同一 runner（只读）。
	runner := a.llmRunner()
	resp, err := client.Ask(ctx, llmRefinePrompt, []llm.Message{{Role: "user", Content: question}}, a.llmTools(), runner)
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// llmRefinePrompt 是修复轮的系统提示：与 llmSystemPrompt 相同的安全边界，外加
// "只输出改写后的 SQL"的输出约束（SQLucid 的修复环要求可 diff 的输出）。
const llmRefinePrompt = llmSystemPrompt +
	"\n\n当前是 SQL 修复轮：用户给出一条 SQL、它的意图说明和修正说明。请只输出改写后的完整 SQL 本身，不要解释，不要 markdown，不要代码围栏。"
