package sqlscript

import (
	"strings"
	"testing"
)

func TestSplitHandlesQuotesAndComments(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"simple", "INSERT INTO a VALUES (1);SELECT 1;", []string{"INSERT INTO a VALUES (1)", "SELECT 1"}},
		{"trailing semicolon", "SELECT 1;\n\n", []string{"SELECT 1"}},
		{"semicolon in string", "INSERT INTO t VALUES ('a;b');SELECT 2", []string{"INSERT INTO t VALUES ('a;b')", "SELECT 2"}},
		{"doubled quote", "INSERT INTO t VALUES ('it''s; fine')", []string{"INSERT INTO t VALUES ('it''s; fine')"}},
		{"mysql escaped quote", `INSERT INTO t VALUES ('it\'s; fine')`, []string{`INSERT INTO t VALUES ('it\'s; fine')`}},
		{"line comment", "SELECT 1 -- ; not a split\n;SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"block comment", "SELECT /* ; */ 1;SELECT 2", []string{"SELECT  1", "SELECT 2"}},
		{"dollar quote", "CREATE FUNCTION f() RETURNS int AS $$ BEGIN; RETURN 1; END $$ LANGUAGE plpgsql;SELECT 3",
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ BEGIN; RETURN 1; END $$ LANGUAGE plpgsql", "SELECT 3"}},
		{"tagged dollar quote", "DO $body$ SELECT ';'; $body$;SELECT 4", []string{"DO $body$ SELECT ';'; $body$", "SELECT 4"}},
		{"backtick identifier", "INSERT INTO `a;b` VALUES (1);SELECT 5", []string{"INSERT INTO `a;b` VALUES (1)", "SELECT 5"}},
		{"empty", "   \n-- only a comment\n", nil},
		{"unterminated string is one statement", "SELECT 'abc;def", []string{"SELECT 'abc;def"}},
	}
	for _, c := range cases {
		got := Split(c.text)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %d statements %q, want %d %q", c.name, len(got), got, len(c.want), c.want)
			continue
		}
		for i := range got {
			if strings.TrimSpace(got[i]) != c.want[i] {
				t.Errorf("%s[%d] = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}
