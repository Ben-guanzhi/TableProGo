package main

import "testing"

func TestNormalizeSafeMode(t *testing.T) {
	cases := map[string]string{
		"":               safeModeOff,
		"off":            safeModeOff,
		"confirmWrites":  safeModeConfirmWrites,
		"confirmNoWhere": safeModeConfirmNoWhere,
		"blockNoWhere":   safeModeBlockNoWhere,
		" blockNoWhere ": safeModeBlockNoWhere,
		"nonsense":       safeModeOff,
		"BLOCKNOWHERE":   safeModeOff, // values are case-sensitive on purpose
	}
	for in, want := range cases {
		if got := normalizeSafeMode(in); got != want {
			t.Errorf("normalizeSafeMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasWhereClauseIgnoresLiteralsAndComments(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{`DELETE FROM t WHERE id = 1`, true},
		{`DELETE FROM t`, false},
		{`UPDATE t SET a = 1`, false},
		{`UPDATE t SET a = 'WHERE'`, false},
		{`UPDATE t SET a = 1 /* WHERE id = 1 */`, false},
		{`UPDATE t SET a = 1 -- WHERE id = 1`, false},
		{`UPDATE t SET a = 1 WHERE id = 1 -- trailing`, true},
		{`UPDATE t SET a = "where"`, false},
		{`UPDATE t SET a = 1 WHERE b = 'x'`, true},
	}
	for _, c := range cases {
		if got := hasWhereClause(c.sql); got != c.want {
			t.Errorf("hasWhereClause(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}

func TestGuardWriteLevels(t *testing.T) {
	read := `SELECT * FROM t`
	insert := `INSERT INTO t VALUES (1)`
	ddl := `DROP TABLE t`
	updateWithWhere := `UPDATE t SET a = 1 WHERE id = 2`
	updateNoWhere := `UPDATE t SET a = 1`
	deleteNoWhere := `DELETE FROM t`

	cases := []struct {
		name    string
		mode    string
		sql     string
		blocked bool
		confirm bool
	}{
		{"off allows everything", safeModeOff, updateNoWhere, false, false},
		{"off ignores DDL", safeModeOff, ddl, false, false},
		{"confirmWrites prompts reads? no", safeModeConfirmWrites, read, false, false},
		{"confirmWrites prompts insert", safeModeConfirmWrites, insert, false, true},
		{"confirmWrites prompts DDL", safeModeConfirmWrites, ddl, false, true},
		{"confirmWrites prompts guarded update", safeModeConfirmWrites, updateWithWhere, false, true},
		{"confirmNoWhere lets insert", safeModeConfirmNoWhere, insert, false, false},
		{"confirmNoWhere lets DDL", safeModeConfirmNoWhere, ddl, false, false},
		{"confirmNoWhere lets guarded update", safeModeConfirmNoWhere, updateWithWhere, false, false},
		{"confirmNoWhere prompts bare update", safeModeConfirmNoWhere, updateNoWhere, false, true},
		{"confirmNoWhere prompts bare delete", safeModeConfirmNoWhere, deleteNoWhere, false, true},
		{"blockNoWhere lets insert", safeModeBlockNoWhere, insert, false, false},
		{"blockNoWhere lets DDL", safeModeBlockNoWhere, ddl, false, false},
		{"blockNoWhere lets guarded update", safeModeBlockNoWhere, updateWithWhere, false, false},
		{"blockNoWhere blocks bare update", safeModeBlockNoWhere, updateNoWhere, true, false},
		{"blockNoWhere blocks bare delete", safeModeBlockNoWhere, deleteNoWhere, true, false},
		{"multi-line update without where", safeModeBlockNoWhere, "UPDATE t\nSET a = 1\n", true, false},
		{"leading comment then update", safeModeBlockNoWhere, "-- note\nUPDATE t SET a = 1", true, false},
	}
	for _, c := range cases {
		got := guardWrite(c.mode, c.sql)
		if got.Blocked != c.blocked || got.NeedsConfirmation != c.confirm {
			t.Errorf("%s: guard = %+v, want blocked=%v confirm=%v", c.name, got, c.blocked, c.confirm)
		}
		if (got.Blocked || got.NeedsConfirmation) && got.Reason == "" {
			t.Errorf("%s: a blocked/confirming verdict must explain itself", c.name)
		}
	}
}

func TestGuardWriteTreatsRedisCommandsAsAllowed(t *testing.T) {
	// DEL/FLUSHDB are not SQL; the guard must not demand a WHERE clause.
	for _, cmd := range []string{"DEL key", "FLUSHDB", "SET k v"} {
		if got := guardWrite(safeModeBlockNoWhere, cmd); got.Blocked {
			t.Errorf("%q should not be blocked as a SQL write: %+v", cmd, got)
		}
	}
}
