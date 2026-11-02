package importfile

import "strings"

// ColumnMap is one file column's landing place in the target table: Table is
// empty when the file column has no counterpart (it is dropped from the
// INSERT, and the mapping says so rather than mis-landing it). Suggested is
// the fuzzy tier's proposal for such a column — advisory only, never applied
// by the import itself; the UI surfaces it for one-click confirmation.
type ColumnMap struct {
	File  string `json:"file"`
	Table string `json:"table,omitempty"`
	// Suggested proposes a table column for an otherwise-unmatched file
	// column (edit distance ≤ FuzzyThreshold after normalization). Empty
	// when the column is matched, when nothing is close enough, or when the
	// proposal would collide with a column already mapped or suggested —
	// the mapping stays a function, and a suggestion must never auto-apply.
	Suggested string `json:"suggested,omitempty"`
}

// MatchColumns pairs the file's columns with the target table's catalog
// columns. The file may name its columns differently (case, punctuation), hold
// them in another order, or carry columns the table does not have; a straight
// "headers are the column list" reading fails on all three.
//
// Matching is a greedy assignment over three tiers, best tier first: exact
// name, then case-insensitive, then normalized (case and the separators space,
// underscore, hyphen folded away). A file column takes the best tier on which
// some table column is still free, first-come-first-served in file order; a
// later file column whose only namesake is already claimed stays unmatched
// instead of displacing the earlier one (the mapping must stay a function).
//
// A headerless file has no names, so the only identity a column has is its
// position: equal arity maps one-to-one by position, anything else reports
// every column unmatched.
func MatchColumns(fileCols, tableCols []string, hasHeader bool) []ColumnMap {
	out := make([]ColumnMap, len(fileCols))
	for i, c := range fileCols {
		out[i] = ColumnMap{File: c}
	}
	if len(out) == 0 || len(tableCols) == 0 {
		return out
	}
	if !hasHeader {
		if len(fileCols) != len(tableCols) {
			return out
		}
		for i := range out {
			out[i].Table = tableCols[i]
		}
		return out
	}

	claimed := make([]bool, len(tableCols))
	tiers := []func(a, b string) bool{exactName, foldName, normName}
	for _, same := range tiers {
		for i := range out {
			if out[i].Table != "" {
				continue // already matched on an earlier tier
			}
			for j, t := range tableCols {
				if claimed[j] || !same(out[i].File, t) {
					continue
				}
				out[i].Table = t
				claimed[j] = true
				break
			}
		}
	}
	suggestFuzzy(out, tableCols, claimed)
	return out
}

// FuzzyThreshold is the maximum normalized edit distance for a suggestion.
// Four edits cover the realistic export-tool drift: a changed token
// ("cust_name" → "customer_name" is 4), a swapped suffix, doubled or dropped
// words. Beyond that a match claim stops being a hint and becomes noise.
const FuzzyThreshold = 4

// suggestFuzzy fills Suggested for file columns the three literal tiers left
// unmatched (K-F1). It is deliberately NOT a matching tier: it never sets
// Table, never claims a column, and never changes the outcome of the first
// three tiers — the greedy tiers' stays-ahead argument and the mapping's
// function property are untouched. One file column proposes at most one table
// column; a table column already mapped (or proposed for an earlier file
// column, in file order) is not proposed again, so accepting every suggestion
// still yields a valid function.
func suggestFuzzy(out []ColumnMap, tableCols []string, claimed []bool) {
	taken := make([]bool, len(tableCols))
	copy(taken, claimed)
	norm := make([]string, len(tableCols))
	for j, t := range tableCols {
		norm[j] = normalizeHeader(t)
	}
	for i := range out {
		if out[i].Table != "" {
			continue
		}
		f := normalizeHeader(out[i].File)
		if f == "" {
			continue
		}
		best, bestDist := -1, FuzzyThreshold+1
		for j, n := range norm {
			if taken[j] || n == "" {
				continue
			}
			if d := editDistance(f, n); d < bestDist {
				best, bestDist = j, d
			}
		}
		// A tie keeps the first candidate (file/scan order): deterministic.
		if best >= 0 {
			out[i].Suggested = tableCols[best]
			taken[best] = true
		}
	}
}

// editDistance is the Levenshtein DP over the normalized names (ch06 sequence
// alignment, deletion-only special case of the alignment recurrence). Header
// names are ≤64 chars, so the O(nm) table is noise next to file parsing.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// exactName is tier one: the spellings agree byte for byte.
func exactName(a, b string) bool { return a == b }

// foldName is tier two: engines fold unquoted identifiers, so Name and name
// are the same column as far as the table is concerned.
func foldName(a, b string) bool { return strings.EqualFold(a, b) }

// normName is tier three: "Order No", "order_no" and "OrderNo" name the same
// column in every export tool, just with different separators.
func normName(a, b string) bool { return normalizeHeader(a) == normalizeHeader(b) }

// normalizeHeader folds case and the three separators export tools disagree on.
func normalizeHeader(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '_', '-':
			continue
		}
		b.WriteRune(toLowerASCII(r))
	}
	return b.String()
}

// toLowerASCII lowers ASCII letters only: header matching must not depend on
// Unicode case tables, and non-ASCII headers (中文列名) are compared verbatim.
func toLowerASCII(r rune) rune {
	if 'A' <= r && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}
