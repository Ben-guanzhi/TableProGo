package sqlformat

import (
	"strings"
	"testing"
)

func TestFormatSelect(t *testing.T) {
	got := Format("select id, name, qty from parts where qty > 10 and name like 'a%' order by id desc limit 20")
	want := strings.Join([]string{
		"SELECT id,",
		"  name,",
		"  qty",
		"FROM parts",
		"WHERE qty > 10 AND name LIKE 'a%'",
		"ORDER BY id DESC",
		"LIMIT 20",
	}, "\n")
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

// Text inside strings and comments must survive byte for byte.
func TestFormatLeavesStringsAndCommentsAlone(t *testing.T) {
	got := Format("select 'select from where', \"my col\", -- select comment\n /* from where */ c from t")
	if !strings.Contains(got, `'select from where'`) {
		t.Fatalf("string mangled: %s", got)
	}
	if !strings.Contains(got, `"my col"`) {
		t.Fatalf("quoted ident mangled: %s", got)
	}
	if !strings.Contains(got, "-- select comment") || !strings.Contains(got, "/* from where */") {
		t.Fatalf("comment mangled: %s", got)
	}
}

// A JOIN variant starts its own line; ON stays attached to it.
func TestFormatJoins(t *testing.T) {
	got := Format("select * from a left join b on a.id = b.id inner join c on b.x = c.x")
	want := strings.Join([]string{
		"SELECT *",
		"FROM a",
		"LEFT JOIN b ON a.id = b.id",
		"INNER JOIN c ON b.x = c.x",
	}, "\n")
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

// Parens keep their contents on one line; a subquery's clauses do not break.
func TestFormatParensStayTogether(t *testing.T) {
	got := Format("select * from t where id in (1, 2, 3) and name in (select name from u)")
	want := strings.Join([]string{
		"SELECT *",
		"FROM t",
		"WHERE id IN (1, 2, 3) AND name IN (SELECT name FROM u)",
	}, "\n")
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

// Dollar-quoted bodies (PL/pgSQL) never get reflowed.
func TestFormatDollarQuotes(t *testing.T) {
	src := "create function f() returns void as $$ begin select 1; end $$ language plpgsql"
	got := Format(src)
	if !strings.Contains(got, "$$ begin select 1; end $$") {
		t.Fatalf("dollar body mangled: %s", got)
	}
}

func TestFormatMultipleStatements(t *testing.T) {
	got := Format("delete from t; select 1")
	want := "DELETE FROM t;\nSELECT 1"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}
