package main

import (
	"fmt"
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

// A leading comment used to defeat the row cap: the cap's SELECT prefix test
// ran before the comment was stripped, so the statement ran uncapped while
// IsReadQuery — which strips trivia first — routed the same statement into the
// capped branch. The user's configured ResultLimit silently never applied.
func TestRowCapAppliesWithLeadingComments(t *testing.T) {
	conn := sqliteDB(t, "conn_cap_comment", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE many (id integer primary key)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO many (id) VALUES (1),(2),(3),(4),(5),(6),(7),(8)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := searchApp(conn)
	a.settings.ResultLimit = 3

	shapes := []struct {
		name string
		sql  string
	}{
		{"plain", "SELECT id FROM many"},
		{"line-comment", "-- note\nSELECT id FROM many"},
		{"block-comment", "/* hint */ SELECT id FROM many"},
		{"oracle-hint", "/*+ MAX_EXECUTION_TIME(1000) */ SELECT id FROM many"},
	}
	for _, s := range shapes {
		res := a.ExecuteQuery(conn.ID, s.sql, conn.Database)
		if res.Error != "" {
			t.Fatalf("%s: %v", s.name, res.Error)
		}
		if len(res.Rows) != 3 || !res.Truncated {
			t.Errorf("%s: got %d rows, truncated %v; want 3 rows and truncated=true",
				s.name, len(res.Rows), res.Truncated)
		}
	}
}

// A CTE's own LIMIT caps the CTE, not the rows the caller sees, so the outer
// statement still needs the cap.
func TestRowCapAppliesAcrossCteLimit(t *testing.T) {
	conn := sqliteDB(t, "conn_cap_cte", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE many (id integer primary key)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := conn.DB.Exec(`INSERT INTO many (id) VALUES (1),(2),(3),(4),(5)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := searchApp(conn)
	a.settings.ResultLimit = 2

	res := a.ExecuteQuery(conn.ID, "WITH c AS (SELECT id FROM many LIMIT 10) SELECT id FROM c", conn.Database)
	if res.Error != "" {
		t.Fatalf("query: %v", res.Error)
	}
	if len(res.Rows) != 2 || !res.Truncated {
		t.Errorf("a CTE-internal LIMIT must not suppress the outer cap: got %d rows, truncated %v",
			len(res.Rows), res.Truncated)
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

// When ResultLimit=0 (explicitly no limit), the non-streaming execute path must
// still bound materialized rows: a SELECT * on a huge table would otherwise OOM
// the client. The hard cap is maxMaterializeRows; exceeding it stops reading
// and marks Truncated.
func TestRunSelectCtxHardBoundsMaterialization(t *testing.T) {
	conn := sqliteDB(t, "conn_hardbound", "scratch")
	a := searchApp(conn)
	a.settings.ResultLimit = 0 // explicitly no limit

	// A recursive CTE generates maxMaterializeRows+1 rows: one more than the
	// hard bound, so the path must truncate.
	const n = maxMaterializeRows + 1
	cte := fmt.Sprintf(
		"WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x < %d) SELECT x FROM seq",
		n,
	)
	result := a.ExecuteQuery(conn.ID, cte, conn.Database)
	if result.Error != "" {
		t.Fatalf("cte: %s", result.Error)
	}
	if len(result.Rows) != maxMaterializeRows {
		t.Errorf("got %d rows, want maxMaterializeRows=%d", len(result.Rows), maxMaterializeRows)
	}
	if !result.Truncated {
		t.Error("the result must be marked Truncated when the hard bound is hit")
	}
}
