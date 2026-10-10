package main

import (
	"testing"
)

// The row cap used to be silent: a grid full of exactly "limit" rows could
// have been the whole table or the first slice of a million. Fetching one row
// past the cap tells the two apart.
func TestRowCapProbesForMoreRows(t *testing.T) {
	conn := sqliteDB(t, "conn_cap", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE five (id integer primary key)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO five (id) VALUES (1),(2),(3),(4),(5)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := searchApp(conn)
	a.settings.ResultLimit = 3

	result := a.ExecuteQuery(conn.ID, "SELECT id FROM five", conn.Database)
	if result.Error != "" {
		t.Fatalf("query: %s", result.Error)
	}
	if len(result.Rows) != 3 || result.RowCount != 3 || !result.Truncated {
		t.Errorf("capped at 3 of five rows: got %d rows, count %d, truncated %v",
			len(result.Rows), result.RowCount, result.Truncated)
	}

	// A statement that caps itself runs untouched - the probe is not ours to
	// promise anything about, so it stays unmarked even past the cap.
	result = a.ExecuteQuery(conn.ID, "SELECT id FROM five ORDER BY id DESC limit 5", conn.Database)
	if result.Error != "" {
		t.Fatalf("self-capped query: %s", result.Error)
	}
	if len(result.Rows) != 5 || result.Truncated {
		t.Errorf("own cap must run untouched: got %d rows, truncated %v", len(result.Rows), result.Truncated)
	}

	// No cap means nothing to truncate.
	a.settings.ResultLimit = 0
	result = a.ExecuteQuery(conn.ID, "SELECT id FROM five", conn.Database)
	if len(result.Rows) != 5 || result.Truncated {
		t.Errorf("uncapped read: got %d rows, truncated %v", len(result.Rows), result.Truncated)
	}

	// Exactly at the cap: the probe comes back short, so the answer is honest.
	a.settings.ResultLimit = 5
	result = a.ExecuteQuery(conn.ID, "SELECT id FROM five", conn.Database)
	if len(result.Rows) != 5 || result.Truncated {
		t.Errorf("table that fits: got %d rows, truncated %v", len(result.Rows), result.Truncated)
	}
}

// A CTE reads rows too. It used to fail the statement classifier and run as a
// command, which executed it and threw the result away: the grid stayed empty
// with no error to explain it. WITH takes the capped-select path now.
func TestCteRunsThroughTheSelectPath(t *testing.T) {
	conn := sqliteDB(t, "conn_cte", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE five (id integer primary key)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO five (id) VALUES (1),(2),(3)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := searchApp(conn)

	const cte = "WITH doubled AS (SELECT id * 2 AS d FROM five) SELECT d FROM doubled ORDER BY d"
	result := a.ExecuteQuery(conn.ID, cte, "")
	if result.Error != "" {
		t.Fatalf("cte query: %s", result.Error)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("a CTE result must come back, got %d rows", len(result.Rows))
	}

	// Row limit and truncation apply to a CTE like to any other read.
	a.settings.ResultLimit = 2
	result = a.ExecuteQuery(conn.ID, cte, "")
	if len(result.Rows) != 2 || !result.Truncated {
		t.Errorf("capped CTE: got %d rows, truncated %v", len(result.Rows), result.Truncated)
	}

	// Read rows out of a CTE do not become writable cells: the source table a
	// grid may write is only recognised for a plain SELECT ... FROM.
	if ref := parseQuerySource(conn.ID, cte, conn.Database); ref.Table != "" {
		t.Errorf("must not map rows read through a CTE back to a table: %+v", ref)
	}
}
