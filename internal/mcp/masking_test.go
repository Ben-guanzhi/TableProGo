package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// 数据掩码: query / sample_table 的输出行经 dbcore/masking (敏感列名整值遮罩 +
// 形态层兜底), 模型看到形状而非真实值; 结果带 masked 标记。
func TestQueryMasksSensitiveColumns(t *testing.T) {
	s, cat := newTestServer(Options{})
	cat.result = &QueryResult{
		Columns: []string{"id", "email", "note"},
		Rows: [][]interface{}{
			{1, "alice@example.com", "手机 13812345678"},
		},
		RowCount: 1,
	}
	text, isErr := callResult(t, s, "query", map[string]interface{}{"connection": "c1", "sql": "SELECT * FROM users"})
	if isErr {
		t.Fatalf("query errored: %s", text)
	}
	var out QueryResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Masked {
		t.Fatal("结果应带 masked 标记")
	}
	row := out.Rows[0]
	// JSON 往返后整数变 float64; 掩码只动字符串, 数值恒等即可。
	if f, ok := row[0].(float64); !ok || f != 1 {
		t.Errorf("非字符串列不应动: %v (%T)", row[0], row[0])
	}
	if email, _ := row[1].(string); strings.Contains(email, "alice@example.com") || !strings.HasPrefix(email, "*") {
		t.Errorf("email 敏感列应整值遮罩: %q", email)
	}
	if note, _ := row[2].(string); strings.Contains(note, "13812345678") {
		t.Errorf("note 里的手机号应被形态层遮罩: %q", note)
	} else if note == "" || !strings.Contains(note, "138****5678") {
		t.Errorf("手机号遮罩形状: %q", note)
	}
}

// MaskQueryResult 导出 (llm.go 的助手路径复用): nil 与非字符串原样通过。
func TestMaskQueryResultLeavesNonStringsAlone(t *testing.T) {
	r := &QueryResult{
		Columns: []string{"id", "score"},
		Rows:    [][]interface{}{{int64(7), 3.14}},
	}
	got := MaskQueryResult(r)
	if !got.Masked {
		t.Fatal("掩码后应置 masked 标记")
	}
	if got.Rows[0][0] != int64(7) || got.Rows[0][1] != 3.14 {
		t.Errorf("数字单元格不应动: %v", got.Rows[0])
	}
	// nil 结果安全。
	if MaskQueryResult(nil) != nil {
		t.Fatal("nil 结果应原样返回")
	}
}
