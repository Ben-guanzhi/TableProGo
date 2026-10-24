// Package sqlscript splits a .sql dump into statements.
//
// The splitter is deliberately a scanner, not a parser: it tracks single/double
// quotes, backtick identifiers, -- and /* */ comments and PostgreSQL dollar
// quotes ($$ / $tag$), because those are exactly the constructs that contain a
// literal ';' in dumps. Anything more exotic (nested block comments, MySQL
// DELIMITER blocks) is left to the user's editor.
package sqlscript

import "strings"

// Split returns the statements in text, in order, with comments and blank
// entries dropped.
func Split(text string) []string {
	var out []string
	var buf strings.Builder
	var quote byte       // 0, '\'', '"', '`'
	var dollarTag string // non-empty while inside $tag$...$tag$
	inLine, inBlock := false, false

	flush := func() {
		if stmt := strings.TrimSpace(buf.String()); stmt != "" {
			out = append(out, stmt)
		}
		buf.Reset()
	}

	for i := 0; i < len(text); i++ {
		ch := text[i]
		var next byte
		if i+1 < len(text) {
			next = text[i+1]
		}

		if inLine {
			if ch == '\n' {
				inLine = false
				buf.WriteByte(ch)
			}
			continue
		}
		if inBlock {
			if ch == '*' && next == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if dollarTag != "" {
			if ch == '$' && strings.HasPrefix(text[i:], dollarTag) {
				buf.WriteString(dollarTag)
				i += len(dollarTag) - 1
				dollarTag = ""
				continue
			}
			buf.WriteByte(ch)
			continue
		}
		if quote != 0 {
			buf.WriteByte(ch)
			if ch == '\\' && quote == '\'' && i+1 < len(text) {
				// MySQL-style escaped quote inside a string.
				buf.WriteByte(next)
				i++
				continue
			}
			if ch == quote {
				if next == quote { // doubled quote stays inside
					buf.WriteByte(next)
					i++
					continue
				}
				quote = 0
			}
			continue
		}

		switch {
		case ch == '-' && next == '-':
			inLine = true
			i++
		case ch == '/' && next == '*':
			inBlock = true
			i++
		case ch == '\'' || ch == '"' || ch == '`':
			quote = ch
			buf.WriteByte(ch)
		case ch == '$':
			if tag, n := dollarQuoteAt(text[i:]); n > 0 {
				dollarTag = tag
				buf.WriteString(tag)
				i += n - 1
				continue
			}
			buf.WriteByte(ch)
		case ch == ';':
			flush()
		default:
			buf.WriteByte(ch)
		}
	}
	flush()
	return out
}

// dollarQuoteAt recognises a PostgreSQL dollar-quote opener ($$ or $tag$) at the
// start of s and returns the tag plus its length.
func dollarQuoteAt(s string) (string, int) {
	if len(s) < 2 || s[0] != '$' {
		return "", 0
	}
	end := strings.IndexByte(s[1:], '$')
	if end < 0 {
		return "", 0
	}
	tag := s[:end+2] // includes both dollars
	inner := tag[1 : len(tag)-1]
	if inner != "" {
		for i := 0; i < len(inner); i++ {
			c := inner[i]
			if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
				return "", 0
			}
		}
	}
	return tag, len(tag)
}
