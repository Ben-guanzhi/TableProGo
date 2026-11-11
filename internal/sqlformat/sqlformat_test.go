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

// W2-c: the three presets share one tokenizer, so a literal and a comment
// survive every layout byte for byte — only line breaks and indent differ.
func TestPresetsShareTokenRules(t *testing.T) {
	src := "select 'a, b', \"my col\", -- keep me\n x from t where y = 'z'"
	for _, preset := range []Preset{PresetDefault, PresetCompact, PresetExpanded} {
		got := FormatWith(src, preset)
		for _, must := range []string{`'a, b'`, `"my col"`, "-- keep me", `'z'`} {
			if !strings.Contains(got, must) {
				t.Fatalf("%s mangled %s: %s", preset, must, got)
			}
		}
	}
}

// The compact preset keeps the select list on one line; the other two break
// each item.
func TestPresetCompactKeepsSelectListInline(t *testing.T) {
	src := "select id, name, qty from parts where qty > 10"
	compact := FormatWith(src, PresetCompact)
	if !strings.HasPrefix(compact, "SELECT id, name, qty") {
		t.Fatalf("compact should keep the list inline: %q", compact)
	}
	if !strings.Contains(compact, "\nFROM parts") {
		t.Fatalf("compact should still break before FROM: %q", compact)
	}

	def := FormatWith(src, PresetDefault)
	if !strings.Contains(def, "SELECT id,\n  name,") {
		t.Fatalf("default should break items with two-space indent: %q", def)
	}
}

// The expanded preset breaks exactly where the default does but indents by
// four spaces, so the two layouts are comparable line by line.
func TestPresetExpandedWidensIndent(t *testing.T) {
	src := "select id, name from parts"
	expanded := FormatWith(src, PresetExpanded)
	if !strings.Contains(expanded, "SELECT id,\n    name") {
		t.Fatalf("expanded should indent four spaces: %q", expanded)
	}
	// Same line count as the default: only the indent differs.
	if a, b := strings.Count(FormatWith(src, PresetDefault), "\n"), strings.Count(expanded, "\n"); a != b {
		t.Fatalf("expanded changed the line count: %d vs %d", a, b)
	}
}

// An unknown or empty preset falls back to the default instead of failing: a
// settings typo must not stop formatting.
func TestParsePresetFallsBackToDefault(t *testing.T) {
	for _, name := range []string{"", "  ", "bogus", "DEFAULT-typo"} {
		if got := ParsePreset(name); got != PresetDefault {
			t.Fatalf("ParsePreset(%q) = %q, want default", name, got)
		}
	}
	if got := ParsePreset(" COMPACT "); got != PresetCompact {
		t.Fatalf("ParsePreset should trim and fold case, got %q", got)
	}
	if got := ParsePreset("Expanded"); got != PresetExpanded {
		t.Fatalf("ParsePreset should fold case, got %q", got)
	}
}

// Format is FormatWith(default): the historical entry point keeps its output.
func TestFormatIsDefaultPreset(t *testing.T) {
	src := "select a, b from t where c = 1"
	if Format(src) != FormatWith(src, PresetDefault) {
		t.Fatal("Format must equal FormatWith(src, PresetDefault)")
	}
}
