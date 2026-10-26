package main

// P2-10 whole-database data search, end to end against the built-in SQLite
// driver (miniredis-shaped: real SQL, real catalog, no server gate needed).

import (
	"context"
	"strings"
	"testing"
)

func collectSearch(t *testing.T, fn func(emit func(SearchDataEvent) error) error) []SearchDataEvent {
	t.Helper()
	var events []SearchDataEvent
	err := fn(func(ev SearchDataEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return events
}

func TestSearchDataFindsTextRows(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])
	if _, err := app.connections["c1"].DB.Exec(`INSERT INTO parts (id, name, qty) VALUES (7, 'needle-in-haystack', 1)`); err != nil {
		t.Fatalf("seed needle: %v", err)
	}

	events := collectSearch(t, func(emit func(SearchDataEvent) error) error {
		return app.SearchTableData(context.Background(), "c1", "", "needle", emit)
	})

	start := events[0]
	if start.Type != "start" || start.Tables < 2 {
		t.Fatalf("start = %+v, want >=2 tables", start)
	}
	done := events[len(events)-1]
	if done.Type != "done" || done.Error != "" {
		t.Fatalf("done = %+v", done)
	}
	var hit *SearchDataEvent
	for i, ev := range events {
		if ev.Type == "table" && ev.Table == "parts" {
			hit = &events[i]
		}
	}
	if hit == nil {
		t.Fatalf("no table event for parts: %+v", events)
	}
	if hit.Hits != 1 || len(hit.Rows) != 1 {
		t.Fatalf("parts event = %+v, want 1 hit row", hit)
	}
	if got, _ := hit.Rows[0][1].(string); !strings.Contains(got, "needle") {
		t.Errorf("hit row = %v, want the needle", hit.Rows[0])
	}
	// The grid needs column metadata with the rows.
	if len(hit.Columns) == 0 || hit.Columns[0].Name != "id" {
		t.Errorf("hit columns = %+v", hit.Columns)
	}
}

// The keyword is literal text: a typed % must not become a wildcard.
func TestSearchDataEscapesKeyword(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])

	events := collectSearch(t, func(emit func(SearchDataEvent) error) error {
		return app.SearchTableData(context.Background(), "c1", "", "100%", emit)
	})
	done := events[len(events)-1]
	if done.Error != "" {
		t.Fatalf("done: %v", done.Error)
	}
	for _, ev := range events {
		if ev.Type == "table" && ev.Hits > 0 {
			t.Fatalf("keyword with a literal %% matched rows in %s", ev.Table)
		}
	}
}

// A cancelled context stops the walk before it reads everything, and says so.
func TestSearchDataCancellation(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events := collectSearch(t, func(emit func(SearchDataEvent) error) error {
		return app.SearchTableData(ctx, "c1", "", "anything", emit)
	})
	done := events[len(events)-1]
	if !done.Cancelled {
		t.Fatalf("done = %+v, want cancelled", done)
	}
	for _, ev := range events {
		if ev.Type == "table" {
			t.Fatalf("a cancelled search still scanned %s", ev.Table)
		}
	}
}

// Views participate only through their text columns; a table with no
// descriptive column is skipped without touching the server as an error.
func TestSearchDataEmptyKeyword(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	events := collectSearch(t, func(emit func(SearchDataEvent) error) error {
		return app.SearchTableData(context.Background(), "c1", "", "   ", emit)
	})
	if len(events) != 1 || events[0].Type != "done" || events[0].Error == "" {
		t.Fatalf("empty keyword events = %+v", events)
	}
}
