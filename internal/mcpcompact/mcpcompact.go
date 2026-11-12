// Package mcpcompact renders a query result as compact tabular text.
//
// Why this exists: a JSON result repeated for every row costs a model a lot of
// context for very little information — every `{"id": 1, "name": "alice"}` carries
// the key once more. A tab-separated block carries the column names once and the
// values as themselves, which is typically a third to a half of the JSON bytes for
// the same rows.
//
// It is NOT a reimplementation of Tabularis's TOON: that is its own specification
// with its own grammar, and shipping something with the same name but a different
// syntax would be worse than shipping nothing. This is the same IDEA (fewer tokens
// per row, columns named once) in a shape that is its own thing and is documented
// as such.
//
// The format:
//
//	# rows=2
//	id	name	qty
//	1	alice	10
//	2	bob	NULL
//	# truncated
//
// A leading `# rows=N` line lets a model know the size without counting, and a
// trailing `# truncated` says the result was cut — a silent cut would let a model
// reason about a partial table as if it were whole.
package mcpcompact

import (
	"fmt"
	"strings"
)

// Result is what the renderer needs: the column names, the rows, and whether the
// row cap cut the result.
type Result struct {
	Columns   []string
	Rows      [][]interface{}
	Truncated bool
	Masked    bool
}

// Render produces the compact block.
//
// Values are stringified the way a database client shows them (nil → NULL) and any
// tab, newline or carriage return inside a value is escaped as \t / \n / \r, so a
// value can never break the row structure. That escaping is the only reason a
// consumer can split on tabs safely, so it is not optional.
func Render(r Result) string {
	var b strings.Builder
	b.Grow(len(r.Rows)*len(r.Columns)*8 + 64)
	fmt.Fprintf(&b, "# rows=%d\n", len(r.Rows))
	for i, col := range r.Columns {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(escapeCell(col))
	}
	b.WriteByte('\n')
	for _, row := range r.Rows {
		for i := range r.Columns {
			if i > 0 {
				b.WriteByte('\t')
			}
			if i < len(row) {
				b.WriteString(escapeCell(formatCell(row[i])))
			} else {
				b.WriteString("NULL")
			}
		}
		b.WriteByte('\n')
	}
	if r.Truncated {
		b.WriteString("# truncated\n")
	}
	if r.Masked {
		b.WriteString("# masked\n")
	}
	return b.String()
}

// formatCell renders one value the way the grid would: nil is NULL, bytes are text,
// everything else is its %v form.
func formatCell(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		// An empty string is not NULL, and a reader must be able to tell them
		// apart: a quoted empty cell is the only way to say "present but empty"
		// in one token.
		if x == "" {
			return `""`
		}
		return x
	case []byte:
		if len(x) == 0 {
			return `""`
		}
		return string(x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", x)
	}
}

// escapeCell makes a value unable to break the tab/newline structure.
func escapeCell(s string) string {
	if !strings.ContainsAny(s, "\t\r\n\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
