package main

// §321 注入面复核回归：保存到表的目标表名/列类型必须走执行路径校验。
// 注意：反引号引用下 "users; DROP..." 是合法标识符（分号是数据不是语句
// 分隔符）——真正危险的是 QuoteIdent 拒绝的字符（NUL/CR/LF/空/超长）。
// 本测试断言内核校验拒绝这些，避免 handler 被改回裸 ident() 回退。

import (
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/ddl"
)

func TestSaveResultTargetRejectsInjection(t *testing.T) {
	// QuoteIdent 拒绝不可引用字符——dot 等不会裸落进 SQL。
	for _, name := range []string{
		"users\nDROP TABLE other",
		"users\rDROP TABLE other",
		"users\x00tail",
		"",
		strings.Repeat("x", 257),
	} {
		if _, err := ddl.ValidatedIdent("mysql", name); err == nil {
			t.Errorf("恶意表名 %q 竟通过 ValidatedIdent", name)
		}
	}

	// 恶意列类型（语句分隔符 / 注释）——ValidateColumnType 拒绝。
	for _, typ := range []string{
		"int; DROP TABLE users;--",
		"TEXT) ON CONFLICT DO NOTHING;--",
		"int /* comment */",
	} {
		if err := ddl.ValidateColumnType("mysql", typ); err == nil {
			t.Errorf("恶意类型 %q 竟通过 ValidateColumnType", typ)
		}
	}

	// 正常值仍通过（不能拒绝合法输入）。
	if _, err := ddl.ValidatedIdent("mysql", "part_name"); err != nil {
		t.Fatalf("合法表名被拒: %v", err)
	}
	for _, typ := range []string{"TEXT", "VARCHAR(120)", "DECIMAL(18,4)"} {
		if err := ddl.ValidateColumnType("mysql", typ); err != nil {
			t.Fatalf("合法类型 %q 被拒: %v", typ, err)
		}
	}
}
