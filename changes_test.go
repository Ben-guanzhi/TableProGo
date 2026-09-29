package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The shape the grid sends: id key plus two editable columns.
func planColumns() []string { return []string{"id", "name", "amount"} }
func planTypes() map[string]string {
	return map[string]string{"id": "int", "name": "varchar(50)", "amount": "decimal(18,2)"}
}

func planChanges(edits ...CellEdit) []RowChange {
	return []RowChange{{Original: []interface{}{7.0, "old", "1.50"}, Edits: edits}}
}

func TestUpdateUsesKeyColumnsAndDialectQuoting(t *testing.T) {
	statements, err := buildUpdateStatements("sqlserver", "[zs].[dbo].[t]", planColumns(),
		[]string{"id"}, planTypes(), planChanges(CellEdit{Column: "name", Value: "O'Brien"}))
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	want := "UPDATE [zs].[dbo].[t] SET [name] = N'O''Brien' WHERE [id] = 7"
	if statements[0] != want {
		t.Errorf("got  %s\nwant %s", statements[0], want)
	}

	mysql, err := buildUpdateStatements("mysql", "`zs`.`t`", planColumns(), []string{"id"},
		planTypes(), planChanges(CellEdit{Column: "amount", Value: "2.75"}))
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if mysql[0] != "UPDATE `zs`.`t` SET `amount` = 2.75 WHERE `id` = 7" {
		t.Errorf("unexpected mysql statement: %s", mysql[0])
	}
}

func TestUpdateNeedsEveryKeyInTheResult(t *testing.T) {
	_, err := buildUpdateStatements("sqlserver", "[t]", []string{"name"}, []string{"id"},
		planTypes(), planChanges(CellEdit{Column: "name", Value: "x"}))
	if err == nil || !strings.Contains(err.Error(), "does not include key column id") {
		t.Errorf("missing key column should be refused, got %v", err)
	}
}

func TestUpdateRefusesKeylessTables(t *testing.T) {
	_, err := buildUpdateStatements("sqlserver", "[t]", planColumns(), nil, planTypes(),
		planChanges(CellEdit{Column: "name", Value: "x"}))
	if err == nil || !strings.Contains(err.Error(), "no primary key") {
		t.Errorf("a table without a key must not be editable, got %v", err)
	}
}

func TestUpdateRefusesColumnsTheQueryInvented(t *testing.T) {
	// "total" is a projection, not a field of the table, so no UPDATE may name it.
	_, err := buildUpdateStatements("sqlserver", "[t]", planColumns(), []string{"id"},
		planTypes(), planChanges(CellEdit{Column: "amount2", Value: "1"}))
	if err == nil || !strings.Contains(err.Error(), "not in the result") {
		t.Errorf("unknown result column should be refused, got %v", err)
	}

	types := planTypes()
	delete(types, "amount")
	_, err = buildUpdateStatements("sqlserver", "[t]", planColumns(), []string{"id"}, types,
		planChanges(CellEdit{Column: "amount", Value: "1"}))
	if err == nil || !strings.Contains(err.Error(), "not a plain column") {
		t.Errorf("a column missing from the table should be refused, got %v", err)
	}
}

func TestValueLiteralsFollowTheColumn(t *testing.T) {
	cases := []struct {
		dbType, colType string
		value           interface{}
		want            string
	}{
		{"postgresql", "varchar", nil, "NULL"},
		{"postgresql", "int", 42.0, "42"},
		{"postgresql", "int", " 42 ", "42"},
		{"postgresql", "int", "", "NULL"},
		{"postgresql", "bigint", 1e18, "1000000000000000000"},
		{"postgresql", "double precision", 0.5, "0.5"},
		{"postgresql", "text", "it's", "'it''s'"},
		{"sqlserver", "nvarchar", "中文", "N'中文'"},
		{"postgresql", "character varying(20)", "x", "'x'"},
		{"oracle", "varchar2", "x", "'x'"},
		{"postgresql", "boolean", true, "1"},
	}
	for _, c := range cases {
		got, err := valueLiteral(c.dbType, c.colType, c.value)
		if err != nil {
			t.Errorf("valueLiteral(%s, %v): %v", c.colType, c.value, err)
			continue
		}
		if got != c.want {
			t.Errorf("valueLiteral(%s, %s, %v) = %s, want %s", c.dbType, c.colType, c.value, got, c.want)
		}
	}

	if _, err := valueLiteral("postgresql", "int", "twelve"); err == nil {
		t.Error("text in a numeric column must be refused, not written as a guess")
	}
	if _, err := valueLiteral("postgresql", "text", "a\x00b"); err == nil {
		t.Error("NUL must not reach the server")
	}
}

func TestKeyOfNULLIsComparedAsNULL(t *testing.T) {
	statements, err := buildUpdateStatements("sqlite", `"t"`, []string{"a", "b"}, []string{"a", "b"},
		map[string]string{"a": "int", "b": "text"},
		[]RowChange{{Original: []interface{}{nil, nil}, Edits: []CellEdit{{Column: "a", Value: 1.0}}}})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if statements[0] != `UPDATE "t" SET "a" = 1 WHERE "a" IS NULL AND "b" IS NULL` {
		t.Errorf("unexpected statement: %s", statements[0])
	}
}

func buildPlan(dbType, name string, changes ...RowChange) ([]string, error) {
	return buildStatements(dbType, name, planColumns(), []string{"id"}, planTypes(), changes)
}

// A new row carries only what the user filled in, so an auto-increment key and
// every column default keep working.
func TestInsertWritesOnlyTheTypedColumns(t *testing.T) {
	statements, err := buildPlan("sqlite", `"t"`, RowChange{Kind: rowInsert, Edits: []CellEdit{
		{Column: "name", Value: "新"}, {Column: "amount", Value: "1.5"},
	}})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if statements[0] != `INSERT INTO "t" ("name", "amount") VALUES ('新', 1.5)` {
		t.Errorf("unexpected statement: %s", statements[0])
	}
}

func TestInsertNeedsSomethingToWrite(t *testing.T) {
	_, err := buildPlan("sqlite", `"t"`, RowChange{Kind: rowInsert})
	if err == nil || !strings.Contains(err.Error(), "at least one value") {
		t.Errorf("an empty new row should be refused, got %v", err)
	}
}

// Inserting is the one write that does not need to find a row, so a keyless
// table can still gain one.
func TestInsertWorksOnAKeylessTable(t *testing.T) {
	statements, err := buildStatements("sqlite", `"t"`, planColumns(), nil, planTypes(),
		[]RowChange{{Kind: rowInsert, Edits: []CellEdit{{Column: "name", Value: "x"}}}})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if statements[0] != `INSERT INTO "t" ("name") VALUES ('x')` {
		t.Errorf("unexpected statement: %s", statements[0])
	}
}

func TestDeleteLocatesTheRowByKey(t *testing.T) {
	statements, err := buildPlan("sqlserver", "[t]", RowChange{
		Kind:     rowDelete,
		Original: []interface{}{7.0, "old", "1.50"},
	})
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if statements[0] != "DELETE FROM [t] WHERE [id] = 7" {
		t.Errorf("unexpected statement: %s", statements[0])
	}
}

func TestDeleteRefusesKeylessTables(t *testing.T) {
	_, err := buildStatements("sqlite", `"t"`, planColumns(), nil, planTypes(),
		[]RowChange{{Kind: rowDelete, Original: []interface{}{7.0, "old", "1.5"}}})
	if err == nil || !strings.Contains(err.Error(), "no primary key") {
		t.Errorf("a table without a key cannot locate a row, got %v", err)
	}
}

// The queue is applied in order, so a delete that frees a unique value has to
// run before the insert that reuses it.
func TestMixedQueueKeepsTheClientOrder(t *testing.T) {
	statements, err := buildPlan("sqlite", `"t"`,
		RowChange{Kind: rowDelete, Original: []interface{}{1.0, "a", "1"}},
		RowChange{Original: []interface{}{2.0, "b", "2"}, Edits: []CellEdit{{Column: "name", Value: "B"}}},
		RowChange{Kind: rowInsert, Edits: []CellEdit{{Column: "name", Value: "c"}}},
	)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	want := []string{
		`DELETE FROM "t" WHERE "id" = 1`,
		`UPDATE "t" SET "name" = 'B' WHERE "id" = 2`,
		`INSERT INTO "t" ("name") VALUES ('c')`,
	}
	if strings.Join(statements, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(statements, "\n"), strings.Join(want, "\n"))
	}
}

// A row whose edits all cancelled out drops out of the batch rather than
// becoming an UPDATE with no SET list.
func TestMixedQueueSkipsRowsWithNothingLeftToSave(t *testing.T) {
	statements, err := buildPlan("sqlite", `"t"`,
		RowChange{Original: []interface{}{1.0, "a", "1"}},
		RowChange{Original: []interface{}{2.0, "b", "2"}, Edits: []CellEdit{{Column: "name", Value: "B"}}},
	)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if len(statements) != 1 || statements[0] != `UPDATE "t" SET "name" = 'B' WHERE "id" = 2` {
		t.Errorf("got %v", statements)
	}
}

// A kind nobody recognises must not fall through to an UPDATE: the row would be
// written differently from what the grid promised.
func TestUnknownRowKindIsRefused(t *testing.T) {
	_, err := buildPlan("sqlite", `"t"`, RowChange{Kind: "delet",
		Original: []interface{}{1.0, "a", "1"}})
	if err == nil || !strings.Contains(err.Error(), "unknown row change kind") {
		t.Errorf("a misspelt kind should be refused, got %v", err)
	}
}

func TestQuerySourceOnlyAcceptsWholeRows(t *testing.T) {
	cases := []struct {
		query string
		want  TableRef
	}{
		{"select * from dbo.t", TableRef{Schema: "dbo", Table: "t"}},
		{"SELECT * FROM zs.dbo.t WHERE id = 1", TableRef{Database: "zs", Schema: "dbo", Table: "t"}},
		{"select t.* from [dbo].[t] t", TableRef{Schema: "dbo", Table: "t"}},
		{"-- pick all\nselect * from t", TableRef{Table: "t"}},
		// A projection has no row to write back, and neither does a join.
		{"select id, name from t", TableRef{}},
		{"select * from t join u on t.id = u.id", TableRef{}},
		{"select * from t group by a", TableRef{}},
		{"update t set a = 1", TableRef{}},
		{"select * from (select * from t) x", TableRef{}},
	}
	for _, c := range cases {
		got := parseQuerySource("c1", c.query, "own")
		if c.want.Table == "" {
			if got.Table != "" {
				t.Errorf("%q should be read-only, got %+v", c.query, got)
			}
			continue
		}
		want := c.want
		want.ConnID = "c1"
		if want.Database == "" {
			want.Database = "own"
		}
		if got != want {
			t.Errorf("parseQuerySource(%q) = %+v, want %+v", c.query, got, want)
		}
	}
}

func TestChangeSetRoundTripsFromJSON(t *testing.T) {
	body := `{"connId":"c1","database":"zs","schema":"dbo","table":"t",
		"columns":["id","name"],
		"changes":[{"original":[7,"old"],"edits":[{"column":"name","value":"新"}]},
			{"kind":"delete","original":[8,"gone"]},
			{"kind":"insert","edits":[{"column":"name","value":"new"}]}]}`
	var set ChangeSet
	if err := json.Unmarshal([]byte(body), &set); err != nil {
		t.Fatalf("the client payload shape is not decodable: %v", err)
	}
	if set.Table != "t" || len(set.Changes) != 3 || set.Changes[0].Edits[0].Value != "新" {
		t.Fatalf("decoded %+v", set)
	}
	// The row kinds the grid queues travel in the same payload.
	if set.Changes[1].Kind != rowDelete || set.Changes[2].Kind != rowInsert {
		t.Errorf("kinds decoded as %q / %q", set.Changes[1].Kind, set.Changes[2].Kind)
	}
	// Numbers arrive as float64, which is what the builder must handle.
	if id, ok := set.Changes[0].Original[0].(float64); !ok || id != 7 {
		t.Errorf("key value decoded as %T", set.Changes[0].Original[0])
	}
}
