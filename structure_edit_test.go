package main

import (
	"strings"
	"testing"
)

// The editor diffs the form against the catalog: add, drop and alter come out
// as kernel-rendered statements; a no-op form renders nothing.
func TestStructureEditBuildsStatements(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])

	// No changes → no statements.
	res, err := app.PreviewStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "parts",
		Columns: []ColumnEdit{
			// The form sends back what the catalog reported, nullability and
			// defaults included — a faithful round-trip must be a no-op.
			{Name: "id", Type: "INTEGER", Nullable: true},
			{Name: "name", Type: "TEXT", DefaultValue: "'x'"},
			{Name: "qty", Type: "INTEGER", Nullable: true},
		},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Statements) != 0 {
		t.Fatalf("no-op form produced %v", res.Statements)
	}

	// Add one column, change one type, drop one column.
	res, err = app.PreviewStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "parts",
		Columns: []ColumnEdit{
			{Name: "id", Type: "INTEGER", Nullable: true},
			{Name: "name", Type: "TEXT", DefaultValue: "'x'"},
			{Name: "note", Type: "text", Nullable: true, IsNew: true},
		},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	joined := strings.Join(res.Statements, "\n")
	if !strings.Contains(joined, `ADD COLUMN "note"`) {
		t.Fatalf("missing add: %v", res.Statements)
	}
	if !strings.Contains(joined, `DROP COLUMN "qty"`) {
		t.Fatalf("missing drop: %v", res.Statements)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
}

// SQLite cannot ALTER a column: the edit comes back as a warning, never as a
// fake statement.
func TestStructureEditWarnsOnUnsupportedAlter(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])

	res, err := app.PreviewStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "parts",
		Columns: []ColumnEdit{
			{Name: "id", Type: "INTEGER", Nullable: true},
			{Name: "name", Type: "VARCHAR(200)", DefaultValue: "'x'"},
			{Name: "qty", Type: "INTEGER", Nullable: true},
		},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Statements) != 0 {
		t.Fatalf("sqlite alter must not produce a statement, got %v", res.Statements)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "SQLite") {
		t.Fatalf("warnings = %v, want the SQLite explanation", res.Warnings)
	}
}

// Triggers: the structure page lists them and the definition comes back.
func TestTriggerListAndDefinition(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])
	if _, e := app.connections["c1"].DB.Exec(`CREATE TABLE parts_log_table (id integer)`); e != nil {
		t.Fatalf("create audit table: %v", e)
	}
	if _, err := app.connections["c1"].DB.Exec(`CREATE TRIGGER parts_log AFTER INSERT ON parts
		BEGIN INSERT INTO parts_log_table (id) VALUES (1); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	structure, err := app.GetTableStructure("c1", "", "", "parts", "table")
	if err != nil {
		t.Fatalf("structure: %v", err)
	}
	found := false
	for _, tr := range structure.Triggers {
		if tr.Name == "parts_log" {
			found = true
		}
	}
	if !found {
		t.Fatalf("trigger missing from structure: %+v", structure.Triggers)
	}

	def, err := app.GetTriggerDefinition("c1", "", "", "parts_log")
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if !strings.Contains(def, "AFTER INSERT") || !strings.Contains(def, "parts_log_table") {
		t.Fatalf("definition = %q", def)
	}
}

// P2-7: the editor diffs indexes the same way it diffs columns — a new key is
// created, a changed one is recreated, an omitted one dropped, and an untouched
// round-trip stays silent.
func TestStructureEditIndexes(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])
	cols := []ColumnEdit{
		{Name: "id", Type: "INTEGER", Nullable: true},
		{Name: "name", Type: "TEXT", DefaultValue: "'x'"},
		{Name: "qty", Type: "INTEGER", Nullable: true},
	}

	// An omitted Indexes list must not drop anything, even with a real index.
	if _, err := app.connections["c1"].DB.Exec(`CREATE INDEX parts_qty ON parts (qty)`); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	res, err := app.PreviewStructureEdit(StructureEditRequest{ConnID: "c1", Table: "parts", Columns: cols})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Statements) != 0 {
		t.Fatalf("nil index list touched indexes: %v", res.Statements)
	}

	// Keep parts_qty exactly as it is, add a unique index, drop nothing.
	res, err = app.PreviewStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "parts", Columns: cols,
		Indexes: []IndexEdit{
			{Name: "parts_qty", Columns: []string{"qty"}},
			{Name: "parts_name", Unique: true, Columns: []string{"name"}},
		},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	joined := strings.Join(res.Statements, "\n")
	if len(res.Statements) != 1 || !strings.Contains(joined, `CREATE UNIQUE INDEX "parts_name" ON "parts" ("name");`) {
		t.Fatalf("statements = %v, want only the new unique index", res.Statements)
	}
	// Apply for real: the next diff compares against a catalog that now has the
	// index, so preview alone proving the statement is not enough.
	if _, err := app.ApplyStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "parts", Columns: cols,
		Indexes: []IndexEdit{
			{Name: "parts_qty", Columns: []string{"qty"}},
			{Name: "parts_name", Unique: true, Columns: []string{"name"}},
		},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Change uniqueness: drop + recreate, and dropping another key by omission.
	res, err = app.PreviewStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "parts", Columns: cols,
		Indexes: []IndexEdit{
			{Name: "parts_qty", Unique: true, Columns: []string{"qty"}},
		},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	joined = strings.Join(res.Statements, "\n")
	if !strings.Contains(joined, `DROP INDEX "parts_name"`) || !strings.Contains(joined, `DROP INDEX "parts_qty"`) {
		t.Fatalf("missing drops: %v", res.Statements)
	}
	if !strings.Contains(joined, `CREATE UNIQUE INDEX "parts_qty"`) {
		t.Fatalf("missing recreate: %v", res.Statements)
	}
}

// SQLite cannot express ALTER TABLE ADD/DROP CONSTRAINT: a foreign-key change
// there is a warning, never a statement the engine would reject.
func TestStructureEditForeignKeyWarnsOnSQLite(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])
	res, err := app.PreviewStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "parts",
		Columns: []ColumnEdit{
			{Name: "id", Type: "INTEGER", Nullable: true},
			{Name: "name", Type: "TEXT", DefaultValue: "'x'"},
			{Name: "qty", Type: "INTEGER", Nullable: true},
		},
		ForeignKeys: []ForeignKeyEdit{{Name: "fk_parts_orders", Columns: []string{"id"}, RefTable: "orders", RefColumns: []string{"id"}}},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Statements) != 0 {
		t.Fatalf("sqlite must not render FK statements: %v", res.Statements)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "重建表") {
		t.Fatalf("warnings = %v, want the rebuild explanation", res.Warnings)
	}
}

// Engine-managed indexes (SQLite's autoindexes behind UNIQUE/PK) are protected:
// an empty desired list removes the user's keys, never the engine's.
func TestStructureEditKeepsPrimaryAndAutoIndexes(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	conn := app.connections["c1"]
	if _, err := conn.DB.Exec(`CREATE TABLE codes (id integer primary key, code text unique)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := conn.DB.Exec(`CREATE INDEX codes_by_id ON codes (code)`); err != nil {
		t.Fatalf("create index: %v", err)
	}
	res, err := app.PreviewStructureEdit(StructureEditRequest{
		ConnID: "c1", Table: "codes",
		Columns: []ColumnEdit{
			{Name: "id", Type: "INTEGER", Nullable: true},
			{Name: "code", Type: "TEXT", Nullable: true},
		},
		Indexes: []IndexEdit{},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	joined := strings.Join(res.Statements, "\n")
	if !strings.Contains(joined, `DROP INDEX "codes_by_id"`) {
		t.Fatalf("the user's index should be dropped: %v", res.Statements)
	}
	if strings.Contains(joined, "sqlite_autoindex") || strings.Contains(joined, `DROP INDEX "PRIMARY"`) {
		t.Fatalf("engine-managed index touched: %v", res.Statements)
	}
}
