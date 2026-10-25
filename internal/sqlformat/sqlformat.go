// Package sqlformat pretty-prints a SQL statement: keywords upper-cased, one
// major clause per line, items after a comma on their own indented line.
//
// It is deliberately a scanner, not a parser — the same stance sqlscript takes
// for splitting dumps. Strings (with ” doubling and backslash escapes), quoted
// identifiers, brackets, -- and /* */ comments and PostgreSQL dollar quotes are
// passed through untouched, so the formatter can never reflow text that is not
// code. Dialect differences (LIMIT vs TOP vs FETCH) do not matter: every major
// keyword is on the same break list, and a keyword that never appears simply
// never fires.
package sqlformat

import (
	"strings"
)

// majorClause breaks the line before these (statement-level sections). The
// JOIN modifiers break too; the JOIN that completes them does not.
var majorClause = map[string]bool{
	"FROM": true, "WHERE": true, "GROUP": true, "HAVING": true,
	"ORDER": true, "LIMIT": true, "OFFSET": true, "FETCH": true,
	"VALUES": true, "SET": true, "RETURNING": true,
	"UNION": true, "INTERSECT": true, "EXCEPT": true, "MINUS": true,
	"LEFT": true, "RIGHT": true, "FULL": true, "INNER": true, "CROSS": true,
}

// uppercase rewrites these words when they appear unquoted. Anything not here
// keeps the user's casing, so function and column names are never mangled.
var keywords = map[string]bool{
	"SELECT": true, "DISTINCT": true, "ALL": true, "AS": true, "FROM": true,
	"WHERE": true, "AND": true, "OR": true, "NOT": true, "NULL": true,
	"INSERT": true, "INTO": true, "VALUES": true, "UPDATE": true, "DELETE": true,
	"SET": true, "CREATE": true, "TABLE": true, "VIEW": true, "INDEX": true,
	"DROP": true, "ALTER": true, "TRUNCATE": true, "JOIN": true,
	"LEFT": true, "RIGHT": true, "FULL": true, "INNER": true, "OUTER": true, "CROSS": true,
	"ON": true, "GROUP": true, "BY": true, "ORDER": true, "HAVING": true,
	"LIMIT": true, "OFFSET": true, "FETCH": true, "FIRST": true, "ROWS": true, "ONLY": true,
	"UNION": true, "INTERSECT": true, "EXCEPT": true, "MINUS": true,
	"CASE": true, "WHEN": true, "THEN": true, "ELSE": true, "END": true,
	"EXISTS": true, "IN": true, "LIKE": true, "BETWEEN": true, "IS": true,
	"ASC": true, "DESC": true, "RETURNING": true,
	"WITH": true, "RECURSIVE": true, "USING": true, "NATURAL": true,
}

// joinModifier opens a "… JOIN" line; the JOIN that completes it stays put.
func joinModifier(word string) bool {
	return word == "LEFT" || word == "RIGHT" || word == "FULL" || word == "INNER" || word == "CROSS"
}

// Format pretty-prints one statement (or script; statements are separated at
// semicolons like sqlscript does). Indent is two spaces per level.
func Format(sql string) string {
	var out strings.Builder
	var line strings.Builder // current output line
	depth := 0               // parenthesis depth
	atLineStart := true      // nothing meaningful on the current line yet
	inSelectList := false    // after SELECT, so top-level commas break lines
	onJoinLine := false      // the line starts with a JOIN modifier

	// The indent is written when a line starts, not when it is flushed: a
	// clause-opening line and its comma-continued items have different indents
	// but go through the same flush.
	startLine := func(indent int) {
		if indent > 0 {
			line.WriteString(strings.Repeat("  ", indent))
		}
	}
	flushLine := func() {
		s := strings.TrimRight(line.String(), " \t")
		line.Reset()
		if s == "" {
			return
		}
		out.WriteString(s)
		out.WriteString("\n")
	}

	newClause := func(word string) {
		flushLine()
		startLine(0)
		line.WriteString(word)
		atLineStart = false
	}

	startLine(0)
	stmtKind := "" // SELECT / UPDATE / DELETE / INSERT: FROM behaves differently
	for _, w := range tokenize(sql) {
		switch w.kind {
		case tokSpace:
			if !atLineStart && line.Len() > 0 && !strings.HasSuffix(line.String(), " ") {
				line.WriteByte(' ')
			}
		case tokComment:
			// Comments ride along verbatim; a block comment ends the line so
			// the statement after it stays readable.
			line.WriteString(w.text)
			if strings.HasPrefix(w.text, "/*") {
				flushLine()
				startLine(0)
				atLineStart = true
			}
		case tokString:
			line.WriteString(w.text)
			atLineStart = false
		case tokPunct:
			switch w.text {
			case "(":
				depth++
				line.WriteString("(")
			case ")":
				depth--
				line.WriteString(")")
			case ",":
				line.WriteString(",")
				if depth == 0 && inSelectList {
					// One select item per line, indented under the clause.
					flushLine()
					startLine(1)
					atLineStart = true
				} else {
					line.WriteByte(' ')
				}
			case ";":
				line.WriteString(";")
				flushLine()
				startLine(0)
				depth = 0
				atLineStart = true
				inSelectList = false
			default:
				line.WriteString(w.text)
				atLineStart = false
			}
		case tokWord:
			upper := strings.ToUpper(w.text)
			switch {
			case upper == "SELECT" || upper == "WITH" || upper == "INSERT" ||
				upper == "UPDATE" || upper == "DELETE":
				if depth > 0 {
					// A subquery's SELECT rides inline like any parenthesised
					// value; breaking it would shred the outer clause.
					line.WriteString(upper)
					atLineStart = false
				} else if !atLineStart {
					newClause(upper)
				} else {
					flushLine()
					startLine(0)
					line.WriteString(upper)
					atLineStart = false
				}
				inSelectList = upper == "SELECT"
				onJoinLine = false
				if depth == 0 {
					stmtKind = upper
				}
			case upper == "JOIN" && depth == 0:
				if !onJoinLine {
					newClause("JOIN")
				} else {
					line.WriteString("JOIN")
				}
				onJoinLine = false
			case majorClause[upper] && depth == 0 && joinModifier(upper):
				newClause(upper)
				onJoinLine = true
			case majorClause[upper] && depth == 0:
				// UPDATE t / DELETE FROM t keep the table on the keyword's own
				// line; only a SELECT's FROM closes a select list.
				keepWithKeyword := upper == "FROM" && (stmtKind == "UPDATE" || stmtKind == "DELETE")
				if keepWithKeyword {
					line.WriteString(upper)
				} else {
					newClause(upper)
				}
				onJoinLine = false
				if upper == "FROM" {
					inSelectList = false
				}
			case keywords[upper]:
				line.WriteString(upper)
				atLineStart = false
			default:
				line.WriteString(w.text)
				atLineStart = false
			}
		}
	}
	flushLine()
	return strings.TrimRight(out.String(), "\n")
}

// token kinds.
const (
	tokWord = iota
	tokString
	tokComment
	tokPunct
	tokSpace
)

type token struct {
	kind int
	text string
}

// tokenize splits SQL into words, strings (kept whole, including their
// quotes), comments, punctuation and whitespace. The scanner tracks the same
// constructs sqlscript does: ” doubling, backslash escapes, "idents",
// `idents`, [idents], line/block comments and $tag$ dollar quotes.
func tokenize(sql string) []token {
	var out []token
	var buf strings.Builder

	flush := func(kind int) {
		if buf.Len() > 0 {
			out = append(out, token{kind: kind, text: buf.String()})
			buf.Reset()
		}
	}
	isWordByte := func(c byte) bool {
		return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
	}
	isPunct := func(c byte) bool {
		return strings.IndexByte("(),;=<>+-*/.%!|&~^?", c) >= 0
	}

	kind := tokSpace
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		var next byte
		if i+1 < len(sql) {
			next = sql[i+1]
		}
		switch kind {
		case tokSpace:
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				buf.WriteByte(c)
				continue
			}
			flush(tokSpace)
		case tokWord:
			if !isWordByte(c) {
				flush(tokWord)
				kind = tokSpace
				i--
				continue
			}
			buf.WriteByte(c)
			continue
		case tokPunct:
			if !isPunct(c) {
				flush(tokPunct)
				kind = tokSpace
				i--
				continue
			}
			buf.WriteByte(c)
			continue
		}

		// kind == tokSpace and c starts a new token. Comments and dollar
		// quotes are checked before the punctuation class: `--`, `/*` and `$`
		// are punctuation bytes that open multi-byte non-code regions.
		switch {
		case c == '-' && next == '-' || c == '/' && next == '*':
			kind = tokComment
			if c == '-' {
				for ; i < len(sql) && sql[i] != '\n'; i++ {
					buf.WriteByte(sql[i])
				}
				if i < len(sql) {
					i-- // the newline stays whitespace
				}
			} else {
				for ; i < len(sql); i++ {
					buf.WriteByte(sql[i])
					if sql[i] == '*' && i+1 < len(sql) && sql[i+1] == '/' {
						buf.WriteByte('/')
						i++
						break
					}
				}
			}
			flush(tokComment)
			kind = tokSpace
		case c == '$':
			if tag, n := dollarQuoteAt(sql[i:]); n > 0 {
				kind = tokString
				buf.WriteString(tag)
				i += n
				for ; i < len(sql); i++ {
					// Check before writing: the closing tag is written whole,
					// so a `$$` cannot lose its second dollar.
					if strings.HasPrefix(sql[i:], tag) {
						buf.WriteString(tag)
						i += len(tag) - 1
						break
					}
					buf.WriteByte(sql[i])
				}
				flush(tokString)
				kind = tokSpace
			} else {
				kind = tokWord // $1 placeholders and friends ride with words
				buf.WriteByte(c)
			}
		case isWordByte(c):
			kind = tokWord
			buf.WriteByte(c)
		case c == '\'' || c == '"' || c == '`':
			kind = tokString
			buf.WriteByte(c)
			i = scanQuoted(sql, i, c, &buf)
			flush(tokString)
			kind = tokSpace
		case c == '[':
			kind = tokString
			buf.WriteByte(c)
			for i++; i < len(sql); i++ {
				buf.WriteByte(sql[i])
				if sql[i] == ']' {
					break
				}
			}
			flush(tokString)
			kind = tokSpace
		case isPunct(c):
			kind = tokPunct
			buf.WriteByte(c)
		default:
			// Control characters and anything else ride with whitespace.
			buf.WriteByte(c)
		}
	}
	flush(kind)
	return out
}

// scanQuoted consumes from the opening quote at i through the closing one,
// honoring doubled quotes and (inside single quotes) backslash escapes.
func scanQuoted(sql string, i int, quote byte, buf *strings.Builder) int {
	for i++; i < len(sql); i++ {
		c := sql[i]
		buf.WriteByte(c)
		if quote == '\'' && c == '\\' && i+1 < len(sql) {
			buf.WriteByte(sql[i+1])
			i++
			continue
		}
		if c == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				buf.WriteByte(quote)
				i++
				continue
			}
			return i
		}
	}
	return i
}

// dollarQuoteAt recognises a PostgreSQL dollar-quote opener ($$ or $tag$).
func dollarQuoteAt(s string) (string, int) {
	if len(s) < 2 || s[0] != '$' {
		return "", 0
	}
	end := strings.IndexByte(s[1:], '$')
	if end < 0 {
		return "", 0
	}
	tag := s[:end+2]
	inner := tag[1 : len(tag)-1]
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "", 0
		}
	}
	return tag, len(tag)
}
