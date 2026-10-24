package changes

import (
	"strings"
	"testing"
)

func types() map[string]string {
	return map[string]string{"id": "integer", "name": "text", "qty": "integer", "note": "text"}
}

func TestBuildRendersUpdateInsertDelete(t *testing.T) {
	statements, err := Build("sqlite", `"items"`, []string{"id", "name"}, []string{"id"}, types(), []RowChange{
		{Kind: KindUpdate, Original: []interface{}{int64(1), "a"}, Edits: []CellEdit{{Column: "name", Value: "b"}}},
		{Kind: KindInsert, Edits: []CellEdit{{Column: "id", Value: float64(2)}, {Column: "name", Value: "c"}}},
		{Kind: KindDelete, Original: []interface{}{int64(3), "d"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 3 {
		t.Fatalf("statements = %v", statements)
	}
	if want := `UPDATE "items" SET "name" = 'b' WHERE "id" = 1`; statements[0] != want {
		t.Errorf("update = %q, want %q", statements[0], want)
	}
	if want := `INSERT INTO "items" ("id", "name") VALUES (2, 'c')`; statements[1] != want {
		t.Errorf("insert = %q, want %q", statements[1], want)
	}
	if want := `DELETE FROM "items" WHERE "id" = 3`; statements[2] != want {
		t.Errorf("delete = %q, want %q", statements[2], want)
	}
}

// A row whose edits all cancelled out renders no statement at all.
func TestBuildSkipsEmptyUpdate(t *testing.T) {
	statements, err := Build("sqlite", `"t"`, []string{"id"}, []string{"id"}, types(), []RowChange{
		{Kind: KindUpdate, Original: []interface{}{int64(1)}, Edits: nil},
	})
	if err == nil {
		t.Fatalf("a batch with nothing to write must fail, got %v", statements)
	}
}

// The result must carry the key column; otherwise an edited row cannot be
// located and the whole queue is refused before anything is rendered.
func TestBuildRequiresKeyColumnInResult(t *testing.T) {
	_, err := Build("sqlite", `"t"`, []string{"name"}, []string{"id"}, types(), []RowChange{
		{Kind: KindUpdate, Original: []interface{}{"a"}, Edits: []CellEdit{{Column: "name", Value: "b"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "key column") {
		t.Fatalf("err = %v", err)
	}
}

// An aliased or computed column is not a plain table column, so it is refused
// rather than written somewhere unexpected.
func TestBuildRefusesAliasedColumn(t *testing.T) {
	_, err := Build("sqlite", `"t"`, []string{"id", "upper(name)"}, []string{"id"}, types(), []RowChange{
		{Kind: KindUpdate, Original: []interface{}{int64(1), "A"}, Edits: []CellEdit{{Column: "upper(name)", Value: "B"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "not a plain column") {
		t.Fatalf("err = %v", err)
	}
}

// An unknown kind must not silently become an UPDATE.
func TestBuildRefusesUnknownKind(t *testing.T) {
	_, err := Build("sqlite", `"t"`, []string{"id"}, []string{"id"}, types(), []RowChange{
		{Kind: "upsert", Original: []interface{}{int64(1)}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown row change kind") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildUpdatesKeepsOnlyEdits(t *testing.T) {
	statements, err := BuildUpdates("sqlite", `"t"`, []string{"id"}, []string{"id"}, types(), []RowChange{
		{Kind: KindInsert, Edits: []CellEdit{{Column: "id", Value: float64(9)}}},
		{Kind: KindUpdate, Original: []interface{}{int64(1)}, Edits: []CellEdit{{Column: "id", Value: float64(2)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 1 || !strings.HasPrefix(statements[0], "UPDATE") {
		t.Fatalf("statements = %v", statements)
	}
}

func TestValueLiteralKeepsBigIntegersExact(t *testing.T) {
	// Past 2^53 a float64 cannot hold the digits; the text path must keep them.
	got, err := ValueLiteral("postgresql", "bigint", "9007199254740993")
	if err != nil {
		t.Fatal(err)
	}
	if got != "9007199254740993" {
		t.Fatalf("literal = %q", got)
	}
	// A cleared numeric cell is NULL, not zero.
	if got, _ := ValueLiteral("sqlite", "integer", "  "); got != "NULL" {
		t.Fatalf("blank numeric = %q", got)
	}
	// Text is quoted (and NUL refused).
	if got, _ := ValueLiteral("sqlite", "text", "a'b"); got != "'a''b'" {
		t.Fatalf("text literal = %q", got)
	}
	if _, err := ValueLiteral("sqlite", "text", "a\x00b"); err == nil {
		t.Fatal("NUL must be refused")
	}
}

func TestPredicateUsesIsNullForNull(t *testing.T) {
	got, err := Predicate("sqlite", "text", `"note"`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != `"note" IS NULL` {
		t.Fatalf("predicate = %q", got)
	}
}
