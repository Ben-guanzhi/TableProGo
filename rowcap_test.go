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
