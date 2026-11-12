// Package sqlrisk answers "how dangerous is this statement?" for a statement a
// MODEL wrote, before a human is asked to run it.
//
// The assistant path differs from the editor path in one way that matters: a user
// typing UPDATE knows what they typed, while a model's proposal arrives as a
// finished sentence the user is being asked to approve. So the proposal card
// carries the same judgement Safe Mode would apply, stated up front, rather than
// letting the confirmation dialog be the first time the risk appears.
//
// This is a classifier, not an enforcer: approval still goes through the same
// Safe Mode gate every other statement does (see safemode.Guard). Duplicating the
// *label* here is deliberate — a card that says "整表改写" and a gate that then
// asks "still run it?" are different moments, and both are useful.
package sqlrisk

import (
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"

	"tablepro-go/internal/safemode"
)

// Level is the coarse verdict the UI colours by.
type Level string

const (
	// LevelRead runs no write at all.
	LevelRead Level = "read"
	// LevelWrite modifies rows, but with a row filter (or an insert): the blast
	// radius is bounded by the statement itself.
	LevelWrite Level = "write"
	// LevelDestructive can rewrite or remove every row, or remove an object.
	LevelDestructive Level = "destructive"
)

// Risk is the assessment shown on a proposal card.
type Risk struct {
	Level Level `json:"level"`
	// Summary is one short line for the card's header.
	Summary string `json:"summary"`
	// Reasons are the specific findings, each a full sentence: a user approving a
	// statement deserves to read WHY it is flagged, not just that it is.
	Reasons []string `json:"reasons,omitempty"`
	// UnboundedScan is set for a read with no row limit: not a write risk, but a
	// query that can pull a whole table into the result.
	UnboundedScan bool `json:"unboundedScan,omitempty"`
}

// Assess classifies one statement.
//
// Order of checks is the order a reader would apply them: is it a write at all,
// then what kind, then whether the kind is the one that needs a row filter.
func Assess(sqlText string) Risk {
	stmt := strings.TrimSpace(sqlText)
	if stmt == "" {
		return Risk{Level: LevelRead, Summary: "空语句"}
	}
	if dbcore.IsReadQuery(stmt) {
		return assessRead(stmt)
	}

	kind := writeKind(stmt)
	switch kind {
	case "insert":
		return Risk{
			Level:   LevelWrite,
			Summary: "插入数据",
			Reasons: []string{"会新增行；不会改动已有行"},
		}
	case "ddl":
		name := ddlVerb(stmt)
		return Risk{
			Level:   LevelDestructive,
			Summary: "结构变更（" + strings.ToUpper(name) + "）",
			Reasons: []string{fmt.Sprintf("%s 会改动表结构，可能不可回滚", strings.ToUpper(name))},
		}
	case "update", "delete", "merge":
		upper := strings.ToUpper(kind)
		if !safemode.HasWhereClause(stmt) {
			return Risk{
				Level:   LevelDestructive,
				Summary: upper + " 没有 WHERE：整表改写",
				Reasons: []string{
					fmt.Sprintf("%s 没有 WHERE 条件，会作用到表的每一行", upper),
					"这类语句通常无法撤销；建议先 SELECT 同样的条件确认范围",
				},
			}
		}
		return Risk{
			Level:   LevelWrite,
			Summary: upper + "（带 WHERE）",
			Reasons: []string{fmt.Sprintf("%s 带 WHERE 条件，影响范围由条件决定", upper)},
		}
	}
	// Anything else that is not a read: be conservative rather than clever.
	return Risk{
		Level:   LevelWrite,
		Summary: "未分类的写语句",
		Reasons: []string{"语句不是只读的，但也不属于已知的写类型：请人工确认它做什么"},
	}
}

// assessRead flags the read that can still hurt: a query with no row limit can
// pull an entire table through the assistant and into a context window.
func assessRead(stmt string) Risk {
	risk := Risk{Level: LevelRead, Summary: "只读查询"}
	if !hasRowLimit(stmt) {
		risk.UnboundedScan = true
		risk.Reasons = append(risk.Reasons,
			"没有 LIMIT/TOP/FETCH：结果可能很大（执行时会按连接的行数上限截断）")
	}
	return risk
}

// hasRowLimit reports whether the statement bounds its own result size.
func hasRowLimit(stmt string) bool {
	clean, ok := stripLiterals(stmt)
	if !ok {
		clean = stmt
	}
	lower := strings.ToLower(clean)
	for _, word := range []string{" limit ", " top ", " fetch first ", " fetch next ", " rownum"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	// A leading TOP (SELECT TOP 10 …) has no space before it.
	if strings.HasPrefix(lower, "select top ") {
		return true
	}
	return false
}

// writeKind classifies a non-read statement. It mirrors safemode's classifier and
// adds INSERT and DDL, which the guard does not need to distinguish.
func writeKind(stmt string) string {
	trimmed := strings.TrimSpace(safemode.StripLeadingComments(stmt))
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "update"):
		return "update"
	case strings.HasPrefix(lower, "delete"):
		return "delete"
	case strings.HasPrefix(lower, "merge"):
		return "merge"
	case strings.HasPrefix(lower, "insert"):
		return "insert"
	case strings.HasPrefix(lower, "drop"), strings.HasPrefix(lower, "truncate"),
		strings.HasPrefix(lower, "alter"), strings.HasPrefix(lower, "create"):
		return "ddl"
	}
	return "other"
}

// ddlVerb names the DDL keyword, for the card's summary.
func ddlVerb(stmt string) string {
	lower := strings.ToLower(strings.TrimSpace(safemode.StripLeadingComments(stmt)))
	for _, verb := range []string{"drop", "truncate", "alter", "create"} {
		if strings.HasPrefix(lower, verb) {
			return verb
		}
	}
	return "ddl"
}

// stripLiterals is a local copy of the literal-blanking scan: this package must
// not depend on safemode's unexported helper, and the scan is ten lines.
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
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}
