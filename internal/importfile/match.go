package importfile

import "strings"

// ColumnMap is one file column's landing place in the target table: Table is
// empty when the file column has no counterpart (it is dropped from the
// INSERT, and the mapping says so rather than mis-landing it).
type ColumnMap struct {
	File  string `json:"file"`
	Table string `json:"table,omitempty"`
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
	return out
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
