package connimport

// 用户可见截断必须是 rune 边界安全——中文 SQL/名字按字节切会产生半个
// UTF-8 字符（乱码）。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShortenIsRuneSafe(t *testing.T) {
	// 60 个中文 = 180 字节；旧实现 s[:57] 会落在某个 UTF-8 字符中间。
	in := strings.Repeat("数", 80)
	got := shorten(in)
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果不是合法 UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("缺省略号: %q", got)
	}
	// 内容应是 57 个中文 + 省略号。
	if want := strings.Repeat("数", 57) + "…"; got != want {
		t.Fatalf("截断内容 = %q, want %q", got, want)
	}
	// 短串原样。
	if got := shorten("短"); got != "短" {
		t.Fatalf("短串被改: %q", got)
	}
}
