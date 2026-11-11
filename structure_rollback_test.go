package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// newConfirmedTestAppWithSQLite builds the App wiring without running any DDL,
// so a test under Safe Mode can seed its own tables through the confirmed path.
func newConfirmedTestAppWithSQLite(t *testing.T, mode string) (*App, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "safe.db")
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: []ConnectionConfig{{ID: "c1", Name: "safe", Type: "sqlite", Database: path}},
		settings:         AppSettings{SafeMode: mode, ResultLimit: 1000},
	}
	if err := app.Connect("c1"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = app.Disconnect("c1") })
	return app, path
}

// TestRenderStructureRollbackColumns covers the column reverse mappings: a column
// the edit added comes back as DROP; a column it dropped is re-added from the
// snapshot; a changed column is restored through AlterColumn; and a rename —
// which the forward editor renders as drop-old + add-new — comes back as
// drop-new + add-old.
func TestRenderStructureRollbackColumns(t *testing.T) {
	t.Run("added column is dropped", func(t *testing.T) {
		res := StructureEditResult{Warnings: []string{}}
		applied := []ColumnEdit{{Name: "id"}, {Name: "title"}, {Name: "new_col", Type: "TEXT", Nullable: true}}
		before := []ColumnEdit{{Name: "id"}, {Name: "title"}}
		stmts, err := renderColumnRollback("postgresql", "public", "book", applied, before, &res)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if len(stmts) != 1 || !strings.Contains(stmts[0], "DROP COLUMN") || !strings.Contains(stmts[0], "new_col") {
			t.Fatalf("statements = %#v, want a single DROP COLUMN new_col", stmts)
		}
	})

	t.Run("dropped column is re-added", func(t *testing.T) {
		res := StructureEditResult{Warnings: []string{}}
		applied := []ColumnEdit{{Name: "id"}}
		before := []ColumnEdit{{Name: "id"}, {Name: "title", Type: "TEXT", Nullable: true, DefaultValue: "'x'"}}
		stmts, err := renderColumnRollback("postgresql", "public", "book", applied, before, &res)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if len(stmts) != 1 || !strings.Contains(stmts[0], "ADD COLUMN") || !strings.Contains(stmts[0], "title") {
			t.Fatalf("statements = %#v, want a single ADD COLUMN title", stmts)
		}
		if !strings.Contains(stmts[0], "DEFAULT 'x'") {
			t.Fatalf("the original default was not restored: %q", stmts[0])
		}
	})

	t.Run("changed column restores the previous definition", func(t *testing.T) {
		res := StructureEditResult{Warnings: []string{}}
		applied := []ColumnEdit{{Name: "id", Type: "TEXT", Nullable: true, DefaultValue: "'x'"}}
		before := []ColumnEdit{{Name: "id", Type: "INTEGER", Nullable: false, DefaultValue: "0"}}
		stmts, err := renderColumnRollback("postgresql", "public", "book", applied, before, &res)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		joined := strings.Join(stmts, "\n")
		for _, want := range []string{"TYPE INTEGER", "SET DEFAULT 0", "SET NOT NULL"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("statements = %#v, want it to restore %q", stmts, want)
			}
		}
	})

	t.Run("renamed column comes back as drop-new + add-old", func(t *testing.T) {
		res := StructureEditResult{Warnings: []string{}}
		applied := []ColumnEdit{{Name: "new_name", Type: "TEXT"}}
		before := []ColumnEdit{{Name: "old_name", Type: "TEXT"}}
		stmts, err := renderColumnRollback("postgresql", "public", "book", applied, before, &res)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if len(stmts) != 2 {
			t.Fatalf("statements = %#v, want drop-new + add-old", stmts)
		}
		if !strings.Contains(stmts[0], "new_name") || !strings.Contains(stmts[0], "DROP COLUMN") {
			t.Fatalf("first statement = %q, want DROP COLUMN new_name", stmts[0])
		}
		if !strings.Contains(stmts[1], "old_name") || !strings.Contains(stmts[1], "ADD COLUMN") {
			t.Fatalf("second statement = %q, want ADD COLUMN old_name", stmts[1])
		}
	})
}

// TestRenderStructureRollbackIndexes covers the index reverse mappings.
func TestRenderStructureRollbackIndexes(t *testing.T) {
	res := StructureEditResult{Warnings: []string{}}
	applied := []IndexEdit{
		{Name: "idx_kept", Columns: []string{"a"}},
		{Name: "idx_added", Columns: []string{"b"}},
		{Name: "idx_changed", Unique: false, Columns: []string{"c"}},
	}
	before := []IndexEdit{
		{Name: "idx_kept", Columns: []string{"a"}},
		{Name: "idx_changed", Unique: true, Columns: []string{"d"}},
		{Name: "idx_dropped", Columns: []string{"e"}},
	}
	stmts, err := renderIndexRollback("postgresql", "public", "book", applied, before, &res)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	for _, want := range []string{
		"DROP INDEX", "idx_added",
		"DROP INDEX", "idx_changed", "CREATE UNIQUE INDEX", "idx_changed", "\"d\"",
		"CREATE INDEX", "idx_dropped", "\"e\"",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("statements = %#v, want it to contain %q", stmts, want)
		}
	}
	if strings.Contains(joined, "idx_kept") {
		t.Fatalf("an unchanged index must not be reversed: %#v", stmts)
	}
}

// TestRenderStructureRollbackForeignKeys covers the foreign key reverse
// mappings on an engine that can express both directions.
func TestRenderStructureRollbackForeignKeys(t *testing.T) {
	res := StructureEditResult{Warnings: []string{}}
	applied := []ForeignKeyEdit{
		{Name: "fk_kept", Columns: []string{"a"}, RefTable: "t", RefColumns: []string{"id"}},
		{Name: "fk_added", Columns: []string{"b"}, RefTable: "t", RefColumns: []string{"id"}},
		{Name: "fk_changed", Columns: []string{"c"}, RefTable: "t2", RefColumns: []string{"id"}, OnDelete: "CASCADE"},
	}
	before := []ForeignKeyEdit{
		{Name: "fk_kept", Columns: []string{"a"}, RefTable: "t", RefColumns: []string{"id"}},
		{Name: "fk_changed", Columns: []string{"c"}, RefTable: "t", RefColumns: []string{"id"}},
		{Name: "fk_dropped", Columns: []string{"e"}, RefTable: "t", RefColumns: []string{"id"}},
	}
	stmts, err := renderForeignKeyRollback("postgresql", "public", "book", applied, before, &res)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	for _, want := range []string{
		"DROP CONSTRAINT", "fk_added",
		"DROP CONSTRAINT", "fk_changed", "ADD CONSTRAINT", "fk_changed", "REFERENCES",
		"ADD CONSTRAINT", "fk_dropped",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("statements = %#v, want it to contain %q", stmts, want)
		}
	}
	if strings.Contains(joined, "fk_kept") {
		t.Fatalf("an unchanged foreign key must not be reversed: %#v", stmts)
	}
}

// TestRenderStructureRollbackUnsupported is the case where the engine cannot
// express the reverse: SQLite cannot alter a column in place or add/drop a
// foreign key without rebuilding the table, so those reverses become warnings
// instead of statements.
func TestRenderStructureRollbackUnsupported(t *testing.T) {
	t.Run("sqlite column change warns instead of rendering", func(t *testing.T) {
		res := StructureEditResult{Warnings: []string{}}
		applied := []ColumnEdit{{Name: "id", Type: "TEXT", Nullable: true}}
		before := []ColumnEdit{{Name: "id", Type: "INTEGER", Nullable: false}}
		stmts, err := renderColumnRollback("sqlite", "main", "book", applied, before, &res)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if len(stmts) != 0 {
			t.Fatalf("statements = %#v, want none", stmts)
		}
		if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "SQLite") {
			t.Fatalf("warnings = %#v, want a SQLite warning", res.Warnings)
		}
	})

	t.Run("sqlite foreign key reverse warns instead of rendering", func(t *testing.T) {
		res := StructureEditResult{Warnings: []string{}}
		applied := []ForeignKeyEdit{{Name: "fk_added", Columns: []string{"b"}, RefTable: "t", RefColumns: []string{"id"}}}
		stmts, err := renderForeignKeyRollback("sqlite", "main", "book", applied, nil, &res)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if len(stmts) != 0 {
			t.Fatalf("statements = %#v, want none", stmts)
		}
		if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "SQLite") {
			t.Fatalf("warnings = %#v, want a SQLite warning", res.Warnings)
		}
	})
}

// TestApplyStructureRollback round-trips a real edit on SQLite: add a column
// through the structure editor, then roll the edit back and confirm the column
// is gone again.
func TestApplyStructureRollback(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE note(id INTEGER PRIMARY KEY, body TEXT)", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}

	before := StructureEditSnapshot{Columns: []ColumnEdit{{Name: "id", Type: "INTEGER"}, {Name: "body", Type: "TEXT", Nullable: true}}}

	// Forward: add a column.
	applied := StructureEditRequest{
		ConnID:  "c1",
		Table:   "note",
		Columns: []ColumnEdit{{Name: "id", Type: "INTEGER"}, {Name: "body", Type: "TEXT", Nullable: true}, {Name: "extra", Type: "TEXT", Nullable: true}},
	}
	if _, err := app.ApplyStructureEdit(applied); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Reverse it.
	rollback := StructureRollbackRequest{
		StructureEditRequest: applied,
		Before:               before,
		Statements:           []string{"ALTER TABLE note ADD COLUMN extra TEXT"},
	}
	res, err := app.ApplyStructureRollback(rollback)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(res.Statements) != 1 || !strings.Contains(res.Statements[0], "DROP COLUMN") || !strings.Contains(res.Statements[0], "extra") {
		t.Fatalf("rollback statements = %#v", res.Statements)
	}

	conn, err := app.connection("c1")
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	state := tableState(t, conn, "SELECT name FROM sqlite_master WHERE type='table' AND name='note'")
	if strings.Contains(strings.Join(state, ","), "extra") {
		t.Fatalf("column extra survived the rollback: %v", state)
	}
}

// TestApplyStructureRollbackSafeModePause confirms the rollback goes through the
// same Safe Mode guard as the forward edit: under confirmWrites it pauses with
// the reason instead of running.
func TestApplyStructureRollbackSafeModePause(t *testing.T) {
	app, _ := newConfirmedTestAppWithSQLite(t, safeModeConfirmWrites)
	if r := app.ExecuteQueryConfirmed("c1", "CREATE TABLE note(id INTEGER PRIMARY KEY, body TEXT, extra TEXT)", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	rollback := StructureRollbackRequest{
		StructureEditRequest: StructureEditRequest{
			ConnID:  "c1",
			Table:   "note",
			Columns: []ColumnEdit{{Name: "id", Type: "INTEGER"}, {Name: "body", Type: "TEXT", Nullable: true}, {Name: "extra", Type: "TEXT", Nullable: true}},
		},
		Before: StructureEditSnapshot{Columns: []ColumnEdit{{Name: "id", Type: "INTEGER"}, {Name: "body", Type: "TEXT", Nullable: true}}},
	}
	_, err := app.ApplyStructureRollback(rollback)
	if _, paused := isNeedsConfirmation(err); !paused {
		t.Fatalf("expected a Safe Mode pause, got %v", err)
	}
	// The confirmed entry point runs it.
	res, err := app.ApplyStructureRollbackConfirmed(rollback)
	if err != nil {
		t.Fatalf("confirmed rollback: %v", err)
	}
	if len(res.Statements) != 1 {
		t.Fatalf("statements = %#v, want one", res.Statements)
	}
}
