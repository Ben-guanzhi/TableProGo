package main

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// tableState reads the rows back as text, so an assertion is about the data the
// database holds rather than about a statement that was expected to run.
func tableState(t *testing.T, conn *Connection, query string) []string {
	t.Helper()
	rows, err := conn.DB.Query(query)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer rows.Close()

	var found []string
	for rows.Next() {
		cols, err := rows.Columns()
		if err != nil {
			t.Fatalf("columns: %v", err)
		}
		values := make([]sql.RawBytes, len(cols))
		targets := make([]interface{}, len(cols))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		parts := make([]string, len(cols))
		for i, raw := range values {
			parts[i] = string(raw)
		}
		found = append(found, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read back: %v", err)
	}
	return found
}

func seedParts(t *testing.T, conn *Connection) {
	t.Helper()
	schema := `CREATE TABLE parts (id integer primary key, name text not null default 'x', qty integer)`
	if _, err := conn.DB.Exec(schema); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO parts (id, name, qty) VALUES (1,'a',10),(2,'b',20)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func partsSet(conn *Connection, changes ...RowChange) ChangeSet {
	return ChangeSet{
		ConnID:   conn.ID,
		Database: conn.Database,
		Table:    "parts",
		Columns:  []string{"id", "name", "qty"},
		Changes:  changes,
	}
}

// The queue the grid sends: a delete, an edited cell, and a new row that only
// names the columns the user filled in.
func TestApplyChangesRunsTheWholeQueue(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	seedParts(t, conn)
	a := searchApp(conn)

	applied, err := a.ApplyChanges(partsSet(conn,
		RowChange{Kind: rowDelete, Original: []interface{}{2.0, "b", 20.0}},
		RowChange{Original: []interface{}{1.0, "a", 10.0}, Edits: []CellEdit{{Column: "qty", Value: "11"}}},
		RowChange{Kind: rowInsert, Edits: []CellEdit{{Column: "name", Value: "c"}, {Column: "qty", Value: "3"}}},
	))
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if applied != 3 {
		t.Errorf("want 3 affected rows, got %d", applied)
	}

	want := []string{"1|a|11", "2|c|3"}
	got := tableState(t, conn, "SELECT id, name, qty FROM parts ORDER BY name")
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("table holds %v, want %v", got, want)
	}
}

// An insert that names no key column must leave the id to the database, which is
// the whole reason the statement lists only what was typed.
func TestApplyChangesLeavesUntypedColumnsToTheDatabase(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	seedParts(t, conn)
	a := searchApp(conn)

	if _, err := a.ApplyChanges(partsSet(conn,
		RowChange{Kind: rowInsert, Edits: []CellEdit{{Column: "name", Value: "d"}}},
	)); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	got := tableState(t, conn, "SELECT id, name, qty FROM parts WHERE name = 'd'")
	if len(got) != 1 || !strings.HasPrefix(got[0], "3|d|") {
		t.Fatalf("unexpected row: %v", got)
	}
}

// One statement that finds no row aborts the batch: a queue applied against data
// that moved underneath it must not land halfway.
func TestApplyChangesRollsBackWhenOneRowIsGone(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	seedParts(t, conn)
	a := searchApp(conn)

	_, err := a.ApplyChanges(partsSet(conn,
		RowChange{Original: []interface{}{1.0, "a", 10.0}, Edits: []CellEdit{{Column: "qty", Value: "99"}}},
		RowChange{Kind: rowDelete, Original: []interface{}{9.0, "gone", 0.0}},
		RowChange{Kind: rowInsert, Edits: []CellEdit{{Column: "name", Value: "never"}}},
	))
	if err == nil || !strings.Contains(err.Error(), "matched no row") {
		t.Fatalf("want a refused delete, got %v", err)
	}

	want := "1|a|10, 2|b|20"
	got := tableState(t, conn, "SELECT id, name, qty FROM parts ORDER BY id")
	if strings.Join(got, ", ") != want {
		t.Errorf("nothing may survive the rollback: table holds %v, want %v", got, want)
	}
}

// A value the column cannot hold is caught while the statements are built, so
// the batch never opens a transaction.
func TestPreviewChangesRefusesBeforeTouchingTheTable(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	seedParts(t, conn)
	a := searchApp(conn)

	_, err := a.PreviewChanges(partsSet(conn,
		RowChange{Kind: rowInsert, Edits: []CellEdit{{Column: "qty", Value: "twelve"}}},
	))
	if err == nil || !strings.Contains(err.Error(), "not a valid") {
		t.Fatalf("want a type refusal, got %v", err)
	}
	got := tableState(t, conn, fmt.Sprintf("SELECT count(*) FROM parts"))
	if len(got) != 1 || got[0] != "2" {
		t.Errorf("table changed under a refused preview: %v", got)
	}
}
