// Package safemode classifies SQL statements for the client's write guard.
//
// It is deliberately a *statement* classifier, not a permission system: the
// database's own grants still decide what is allowed. The guard only stops a
// stray UPDATE/DELETE typed into the query editor from running against the
// wrong table.
//
// Levels:
//   - Off             — no gate
//   - ConfirmWrites   — every writing statement needs an explicit confirmation
//   - ConfirmNoWhere  — UPDATE/DELETE without WHERE needs confirmation
//   - BlockNoWhere    — UPDATE/DELETE without WHERE is refused outright
//
// DDL (CREATE/ALTER/DROP/TRUNCATE) and INSERT are not "no WHERE" statements:
// they have no row filter to lose, so the two WHERE-oriented levels let them
// through and only ConfirmWrites covers them.
package safemode

import (
	"strings"

	"github.com/dbcore/dbcore"
)

// Levels.
const (
	Off            = "off"
	ConfirmWrites  = "confirmWrites"
	ConfirmNoWhere = "confirmNoWhere"
	BlockNoWhere   = "blockNoWhere"
)

// Normalize maps unknown/empty values onto Off, so a settings file written by an
// older version never blocks the app.
func Normalize(mode string) string {
	switch strings.TrimSpace(mode) {
	case ConfirmWrites, ConfirmNoWhere, BlockNoWhere:
		return strings.TrimSpace(mode)
	}
	return Off
}

// Verdict is the answer for one statement under one mode.
type Verdict struct {
	Blocked           bool
	NeedsConfirmation bool
	Reason            string
}

// Guard applies mode to a statement. Reads always pass.
func Guard(mode, query string) Verdict {
	mode = Normalize(mode)
	if mode == Off || dbcore.IsReadQuery(query) {
		return Verdict{}
	}
	kind := writeKind(query)
	if mode == ConfirmWrites {
		return Verdict{NeedsConfirmation: true, Reason: "Safe Mode: every statement that modifies data needs confirmation"}
	}
	if kind != "update" && kind != "delete" && kind != "merge" {
		return Verdict{} // INSERT / DDL carry no row filter to lose
	}
	if HasWhereClause(query) {
		return Verdict{}
	}
	switch mode {
	case BlockNoWhere:
		return Verdict{Blocked: true, Reason: "Safe Mode blocked a " + strings.ToUpper(kind) + " without a WHERE clause"}
	default: // ConfirmNoWhere
		return Verdict{NeedsConfirmation: true, Reason: "Safe Mode: a " + strings.ToUpper(kind) + " without a WHERE clause will rewrite every row"}
	}
}

// writeKind classifies a statement for the WHERE-oriented levels.
func writeKind(query string) string {
	trimmed := strings.TrimSpace(StripLeadingComments(query))
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "update"):
		return "update"
	case strings.HasPrefix(lower, "delete"):
		return "delete"
	case strings.HasPrefix(lower, "merge"):
		return "merge"
	}
	return "other"
}

// HasWhereClause reports whether the statement has a real WHERE keyword,
// ignoring occurrences inside string literals, quoted identifiers and comments
// ("DELETE FROM t WHERE note = 'WHERE is not here'" must not count twice, and a
// commented-out WHERE must not make an unguarded statement look safe).
func HasWhereClause(query string) bool {
	clean := StripLeadingComments(query)
	cleaned, ok := stripLiterals(clean)
	if !ok {
		return false
	}
	lower := strings.ToLower(cleaned)
	for i := 0; i+5 <= len(lower); i++ {
		if lower[i:i+5] != "where" {
			continue
		}
		before := byte(' ')
		if i > 0 {
			before = lower[i-1]
		}
		after := byte(' ')
		if i+5 < len(lower) {
			after = lower[i+5]
		}
		if !isIdentByte(before) && !isIdentByte(after) {
			return true
		}
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// StripLeadingComments removes whitespace and leading -- / /* */ comments so the
// statement prefix can be inspected.
func StripLeadingComments(query string) string {
	s := query
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if idx := strings.IndexByte(s, '\n'); idx >= 0 {
				s = s[idx+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if idx := strings.Index(s, "*/"); idx >= 0 {
				s = s[idx+2:]
				continue
			}
			return ""
		}
		return s
	}
}

// stripLiterals blanks string literals, quoted identifiers and comments so
// keyword scans cannot be fooled by their content. ok=false means the text ends
// inside an unterminated literal/comment.
func stripLiterals(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'', '"', '`':
			quote := c
			b.WriteByte(' ')
			i++
			for i < len(s) {
				if s[i] == quote {
					// Doubled quote is an escaped quote, stay inside.
					if i+1 < len(s) && s[i+1] == quote {
						i += 2
						continue
					}
					break
				}
				i++
			}
			if i >= len(s) {
				return "", false
			}
			b.WriteByte(' ')
		case '-':
			if i+1 < len(s) && s[i+1] == '-' {
				for i < len(s) && s[i] != '\n' {
					i++
				}
				b.WriteByte('\n')
			} else {
				b.WriteByte(c)
			}
		case '/':
			if i+1 < len(s) && s[i+1] == '*' {
				end := strings.Index(s[i+2:], "*/")
				if end < 0 {
					return "", false
				}
				i += 2 + end + 1
				b.WriteByte(' ')
			} else {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}
