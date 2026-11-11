package main

import (
	"strings"
	"testing"
)

// foreignKeyInfo builds a catalog row for the render tests.
func foreignKeyInfo(name string, cols, refCols []string, refTable string) ForeignKeyInfo {
	return ForeignKeyInfo{
		Name:       name,
		Columns:    cols,
		RefSchema:  "",
		RefTable:   refTable,
		RefColumns: refCols,
		OnDelete:   "",
		OnUpdate:   "",
	}
}

// TestRenderERFKEditPreviewPerEngine covers the preview rendering for the
// engines the relationship diagram can target: one SQL engine renders the
// ALTER statement, and SQLite — which has no ALTER TABLE ... ADD/DROP
// CONSTRAINT — refuses with a user-facing Chinese error rather than emitting a
// statement it would reject.
func TestRenderERFKEditPreviewPerEngine(t *testing.T) {
	req := ERFKEditRequest{
		Table:      "book",
		Action:     "add",
		Name:       "fk_book_author",
		Columns:    []string{"author_id"},
		RefTable:   "author",
		RefColumns: []string{"id"},
		OnDelete:   "CASCADE",
	}

	t.Run("postgresql renders ADD CONSTRAINT", func(t *testing.T) {
		res, err := renderERFKEdit("postgresql", "public", req, nil)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		want := `ALTER TABLE "public"."book" ADD CONSTRAINT "fk_book_author" FOREIGN KEY ("author_id") REFERENCES "author" ("id") ON DELETE CASCADE;`
		if len(res.Statements) != 1 || res.Statements[0] != want {
			t.Fatalf("statements = %#v, want [%s]", res.Statements, want)
		}
		// The reverse of an add is a drop by name.
		wantRollback := `ALTER TABLE "public"."book" DROP CONSTRAINT "fk_book_author";`
		if len(res.Rollback) != 1 || res.Rollback[0] != wantRollback {
			t.Fatalf("rollback = %#v, want [%s]", res.Rollback, wantRollback)
		}
	})

	t.Run("mysql renders the FOREIGN KEY form", func(t *testing.T) {
		res, err := renderERFKEdit("mysql", "db", req, nil)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if len(res.Statements) != 1 {
			t.Fatalf("statements = %#v", res.Statements)
		}
		got := res.Statements[0]
		want := "ALTER TABLE `db`.`book` ADD CONSTRAINT `fk_book_author` FOREIGN KEY (`author_id`) REFERENCES `author` (`id`) ON DELETE CASCADE;"
		if got != want {
			t.Fatalf("statement = %q, want %q", got, want)
		}
	})

	t.Run("sqlite refuses to add a key", func(t *testing.T) {
		_, err := renderERFKEdit("sqlite", "main", req, nil)
		if err == nil {
			t.Fatal("expected SQLite to refuse adding a foreign key")
		}
		if !strings.Contains(err.Error(), "SQLite") || !strings.Contains(err.Error(), "添加外键") {
			t.Fatalf("error = %q, want a Chinese refusal naming SQLite", err.Error())
		}
	})
}

// TestRenderERFKEditDrop confirms a drop reads the original definition from the
// catalog so its rollback can re-add exactly what was removed.
func TestRenderERFKEditDrop(t *testing.T) {
	current := []ForeignKeyInfo{foreignKeyInfo("fk_book_author", []string{"author_id"}, []string{"id"}, "author")}
	res, err := renderERFKEdit("postgresql", "public", ERFKEditRequest{
		Table:  "book",
		Action: "drop",
		FKName: "fk_book_author",
	}, current)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `ALTER TABLE "public"."book" DROP CONSTRAINT "fk_book_author";`
	if len(res.Statements) != 1 || res.Statements[0] != want {
		t.Fatalf("statements = %#v, want [%s]", res.Statements, want)
	}
	wantRollback := `ALTER TABLE "public"."book" ADD CONSTRAINT "fk_book_author" FOREIGN KEY ("author_id") REFERENCES "author" ("id");`
	if len(res.Rollback) != 1 || res.Rollback[0] != wantRollback {
		t.Fatalf("rollback = %#v, want [%s]", res.Rollback, wantRollback)
	}
}

// TestRenderERFKEditRollback covers both rollback directions: undoing an applied
// add (the key is still in the catalog) and undoing an applied drop (the key is
// gone, so the request carries the definition to restore).
func TestRenderERFKEditRollback(t *testing.T) {
	t.Run("undo an add", func(t *testing.T) {
		current := []ForeignKeyInfo{foreignKeyInfo("fk_book_author", []string{"author_id"}, []string{"id"}, "author")}
		res, err := renderERFKEdit("postgresql", "public", ERFKEditRequest{
			Table:      "book",
			Action:     "rollback",
			FKName:     "fk_book_author",
			RollbackOf: "add",
		}, current)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		want := `ALTER TABLE "public"."book" DROP CONSTRAINT "fk_book_author";`
		if len(res.Statements) != 1 || res.Statements[0] != want {
			t.Fatalf("statements = %#v, want [%s]", res.Statements, want)
		}
	})

	t.Run("undo a drop restores the original definition", func(t *testing.T) {
		res, err := renderERFKEdit("postgresql", "public", ERFKEditRequest{
			Table:      "book",
			Action:     "rollback",
			FKName:     "fk_book_author",
			RollbackOf: "drop",
			Name:       "fk_book_author",
			Columns:    []string{"author_id"},
			RefTable:   "author",
			RefColumns: []string{"id"},
			OnUpdate:   "SET NULL",
		}, nil)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		want := `ALTER TABLE "public"."book" ADD CONSTRAINT "fk_book_author" FOREIGN KEY ("author_id") REFERENCES "author" ("id") ON UPDATE SET NULL;`
		if len(res.Statements) != 1 || res.Statements[0] != want {
			t.Fatalf("statements = %#v, want [%s]", res.Statements, want)
		}
		wantRollback := `ALTER TABLE "public"."book" DROP CONSTRAINT "fk_book_author";`
		if len(res.Rollback) != 1 || res.Rollback[0] != wantRollback {
			t.Fatalf("rollback = %#v, want [%s]", res.Rollback, wantRollback)
		}
	})
}

// TestRenderERFKEditRefusesUnsupportedEngine is the refusal case for an engine
// that cannot add or drop a foreign key on an existing table.
func TestRenderERFKEditRefusesUnsupportedEngine(t *testing.T) {
	current := []ForeignKeyInfo{foreignKeyInfo("fk_book_author", []string{"author_id"}, []string{"id"}, "author")}
	for _, tc := range []struct {
		name string
		req  ERFKEditRequest
	}{
		{"add", ERFKEditRequest{Table: "book", Action: "add", Name: "fk_x", Columns: []string{"author_id"}, RefTable: "author", RefColumns: []string{"id"}}},
		{"drop", ERFKEditRequest{Table: "book", Action: "drop", FKName: "fk_book_author"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderERFKEdit("sqlite", "main", tc.req, current)
			if err == nil {
				t.Fatalf("expected a refusal, got none")
			}
			if !strings.Contains(err.Error(), "SQLite") {
				t.Fatalf("error = %q, want it to name SQLite", err.Error())
			}
		})
	}
}

// TestRenderERFKEditValidation rejects input that would smuggle SQL or describe
// an impossible key before any statement is rendered.
func TestRenderERFKEditValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  ERFKEditRequest
	}{
		{"missing name", ERFKEditRequest{Table: "book", Action: "add", Columns: []string{"a"}, RefTable: "t", RefColumns: []string{"id"}}},
		{"empty columns", ERFKEditRequest{Table: "book", Action: "add", Name: "fk", RefTable: "t", RefColumns: []string{"id"}}},
		{"mismatched column counts", ERFKEditRequest{Table: "book", Action: "add", Name: "fk", Columns: []string{"a", "b"}, RefTable: "t", RefColumns: []string{"id"}}},
		{"missing ref table", ERFKEditRequest{Table: "book", Action: "add", Name: "fk", Columns: []string{"a"}, RefColumns: []string{"id"}}},
		{"bad identifier", ERFKEditRequest{Table: "book", Action: "add", Name: "fk\nDROP TABLE x", Columns: []string{"a"}, RefTable: "t", RefColumns: []string{"id"}}},
		{"bad action", ERFKEditRequest{Table: "book", Action: "add", Name: "fk", Columns: []string{"a"}, RefTable: "t", RefColumns: []string{"id"}, OnDelete: "DROP TABLE users"}},
		{"unknown action", ERFKEditRequest{Table: "book", Action: "rename", Name: "fk", Columns: []string{"a"}, RefTable: "t", RefColumns: []string{"id"}}},
		{"drop without name", ERFKEditRequest{Table: "book", Action: "drop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := renderERFKEdit("postgresql", "public", tc.req, nil); err == nil {
				t.Fatalf("expected an error for %+v", tc.req)
			}
		})
	}
}

// TestApplyERFKEditRefusesOnSQLite drives the App entry point end to end: SQLite
// cannot attach a foreign key to an existing table, so both preview and apply
// refuse with the engine named in the message.
func TestApplyERFKEditRefusesOnSQLite(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	for _, q := range []string{
		"CREATE TABLE author(id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
		"CREATE TABLE book(id INTEGER PRIMARY KEY, author_id INTEGER, FOREIGN KEY(author_id) REFERENCES author(id))",
	} {
		if r := app.ExecuteQuery("c1", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}
	req := ERFKEditRequest{
		ConnID:     "c1",
		Table:      "book",
		Action:     "add",
		Name:       "fk_book_title",
		Columns:    []string{"id"},
		RefTable:   "author",
		RefColumns: []string{"id"},
	}
	if _, err := app.PreviewERFKEdit(req); err == nil {
		t.Fatal("expected the SQLite preview to refuse")
	}
	if _, err := app.ApplyERFKEdit(req); err == nil {
		t.Fatal("expected the SQLite apply to refuse")
	}

	// Dropping the table's existing key is refused for the same reason.
	drop := ERFKEditRequest{ConnID: "c1", Table: "book", Action: "drop", FKName: "fk_book_author"}
	if _, err := app.ApplyERFKEdit(drop); err == nil {
		t.Fatal("expected the SQLite drop to refuse")
	}
}
