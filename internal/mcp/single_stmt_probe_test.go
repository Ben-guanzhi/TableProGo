package mcp

import "testing"

// Boundary cases for the multi-statement guard. The rule is "one statement",
// not "one semicolon": trailing punctuation and empty statements are what
// editors and models routinely emit, and rejecting them would make the tool
// unusable without closing any hole. A ';' only counts as a separator when
// real content follows it.
func TestSingleStatementEdgeCases(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"SELECT 1", true},
		{"SELECT 1;", true},   // trailing semicolon: punctuation
		{"SELECT 1; ", true},  // ... with trailing space
		{"SELECT 1;;", true},  // an empty statement is not a second one
		{"SELECT 1;;;", true}, // any number of empty statements
		{"SELECT 1; -- done", true},
		{"SELECT 1; /* done */", true},
		{"BEGIN", true},
		{"SELECT 'a;b'", true},
		{"SELECT 1; DROP TABLE t", false},
		{"SELECT 1; SELECT 2", false},
		{"SELECT 1; -- note\nDROP TABLE t", false}, // a comment does not hide it
		{"SELECT 1; /* note */ DROP TABLE t", false},
		{"", true},
		{"   ", true},
		{";", true},
	}
	for _, c := range cases {
		if got := isSingleStatement(c.sql); got != c.want {
			t.Errorf("isSingleStatement(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}
