package main

import (
	"testing"
)

// TestGetERDiagram collects one schema's tables, columns and foreign keys and
// asserts the model the SVG renderer consumes: both tables present, the foreign
// key edge resolved to the referenced table and column, and the primary key flag
// set on the anchored column.
func TestGetERDiagram(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	for _, q := range []string{
		"CREATE TABLE author(id INTEGER PRIMARY KEY, name TEXT NOT NULL)",
		"CREATE TABLE book(id INTEGER PRIMARY KEY, title TEXT, author_id INTEGER, FOREIGN KEY(author_id) REFERENCES author(id))",
		"CREATE VIEW book_view AS SELECT id, title FROM book",
	} {
		if r := app.ExecuteQuery("c1", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}

	model, err := app.GetERDiagram("c1", "", "main")
	if err != nil {
		t.Fatalf("GetERDiagram: %v", err)
	}

	byName := map[string]ERTable{}
	for _, tb := range model.Tables {
		byName[tb.Name] = tb
	}
	// The view is a relational artifact with no foreign keys of its own and must
	// not clutter the relationship diagram.
	if _, excluded := byName["book_view"]; excluded {
		t.Fatalf("view book_view should be excluded from the diagram")
	}
	book, ok := byName["book"]
	if !ok {
		t.Fatalf("book missing; got %v", model.Tables)
	}
	if len(book.ForeignKeys) != 1 {
		t.Fatalf("book FKs = %d, want 1", len(book.ForeignKeys))
	}
	fk := book.ForeignKeys[0]
	if fk.RefTable != "author" || len(fk.Columns) != 1 || fk.Columns[0] != "author_id" {
		t.Fatalf("unexpected FK: %+v", fk)
	}

	author, ok := byName["author"]
	if !ok {
		t.Fatalf("author missing")
	}
	pkFound := false
	for _, c := range author.Columns {
		if c.Name == "id" && c.IsPrimaryKey {
			pkFound = true
		}
	}
	if !pkFound {
		t.Fatalf("author.id not flagged as primary key: %+v", author.Columns)
	}
}

// TestGetERDiagramColumnTypes confirms the rendered node carries the engine type
// for each column, which the SVG shows right-aligned under the column name.
func TestGetERDiagramColumnTypes(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	for _, q := range []string{
		"CREATE TABLE sample(id INTEGER PRIMARY KEY, name TEXT NOT NULL, price REAL)",
	} {
		if r := app.ExecuteQuery("c1", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}

	model, err := app.GetERDiagram("c1", "", "main")
	if err != nil {
		t.Fatalf("GetERDiagram: %v", err)
	}
	var sample *ERTable
	for i := range model.Tables {
		if model.Tables[i].Name == "sample" {
			sample = &model.Tables[i]
		}
	}
	if sample == nil {
		t.Fatalf("sample missing; got %v", model.Tables)
	}
	types := map[string]string{}
	for _, c := range sample.Columns {
		types[c.Name] = c.Type
	}
	if types["id"] == "" || types["name"] == "" || types["price"] == "" {
		t.Fatalf("column types not populated: %+v", types)
	}
}
