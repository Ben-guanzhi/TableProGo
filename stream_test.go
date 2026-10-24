package main

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// collectStream runs StreamQuery and returns the events it produced.
func collectStream(t *testing.T, app *App, query string) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	err := app.StreamQuery(context.Background(), "c1", query, "", func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return events
}

func TestStreamEmitsColumnsRowsDone(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	events := collectStream(t, app, "SELECT id, name FROM t ORDER BY id")

	if len(events) != 3 {
		t.Fatalf("events = %d, want columns+rows+done: %+v", len(events), events)
	}
	if events[0].Type != "columns" || len(events[0].Columns) != 2 {
		t.Fatalf("first event = %+v", events[0])
	}
	if events[1].Type != "rows" || len(events[1].Rows) != 2 {
		t.Fatalf("rows event = %+v", events[1])
	}
	if events[2].Type != "done" || events[2].RowCount != 2 || events[2].Error != "" {
		t.Fatalf("done event = %+v", events[2])
	}
}

func TestStreamMarksTruncationAndStopsAtLimit(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	// Insert more rows than the configured cap.
	for i := 3; i <= 12; i++ {
		q := "INSERT INTO t(id,name) VALUES (" + strconv.Itoa(i) + ",'x')"
		if r := app.ExecuteQuery("c1", q, ""); r.Error != "" {
			t.Fatalf("seed: %s", r.Error)
		}
	}
	app.settings.ResultLimit = 5

	events := collectStream(t, app, "SELECT id FROM t ORDER BY id")
	if len(events) < 2 {
		t.Fatalf("events = %+v", events)
	}
	done := events[len(events)-1]
	if done.Type != "done" || done.RowCount != 5 || !done.Truncated {
		t.Fatalf("done = %+v, want rowCount=5 truncated", done)
	}
	rows := 0
	for _, ev := range events {
		rows += len(ev.Rows)
	}
	if rows != 5 {
		t.Fatalf("streamed %d rows, want 5", rows)
	}
}

func TestStreamBatchesLargeResults(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE big(id INTEGER)", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	// Seed via a recursive CTE (portable in SQLite).
	if r := app.ExecuteQuery("c1",
		"WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < 450) INSERT INTO big(id) SELECT x FROM n", ""); r.Error != "" {
		t.Fatalf("seed: %s", r.Error)
	}
	events := collectStream(t, app, "SELECT id FROM big ORDER BY id")
	batches := 0
	rows := 0
	for _, ev := range events {
		if ev.Type == "rows" {
			batches++
			rows += len(ev.Rows)
		}
	}
	if rows != 450 {
		t.Fatalf("rows = %d", rows)
	}
	if batches != 3 { // 200 + 200 + 50
		t.Fatalf("batches = %d, want 3 (streamBatchSize=%d)", batches, streamBatchSize)
	}
}

func TestStreamWriteFallsBackToBuffered(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	events := collectStream(t, app, "UPDATE t SET name = 'z' WHERE id = 1")
	if len(events) == 0 {
		t.Fatal("no events")
	}
	done := events[len(events)-1]
	if done.Type != "done" || done.Error != "" {
		t.Fatalf("done = %+v", done)
	}
	if r := app.ExecuteQuery("c1", "SELECT name FROM t WHERE id = 1", ""); r.Rows[0][0] != "z" {
		t.Fatalf("write did not apply: %+v", r)
	}
}

func TestStreamSafeModeBlocksWithoutRunning(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeBlockNoWhere)
	events := collectStream(t, app, "DELETE FROM t")
	if len(events) != 1 || events[0].Type != "error" || events[0].Error == "" {
		t.Fatalf("events = %+v", events)
	}
	if r := app.ExecuteQuery("c1", "SELECT id FROM t", ""); r.RowCount != 2 {
		t.Fatalf("blocked delete ran: %+v", r)
	}
}

// A cancel during a stream must end it with an error rather than reading on.
func TestLiveStreamCancelStopsEarly(t *testing.T) {
	app := newLivePGApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rowsSeen := 0
	start := time.Now()
	err := app.StreamQuery(ctx, "c1", "SELECT i FROM generate_series(1,1000000) i", "", func(ev StreamEvent) error {
		rowsSeen += len(ev.Rows)
		if rowsSeen >= streamBatchSize {
			cancel() // client-side abort after the first batch
		}
		return nil
	})
	if err == nil && rowsSeen >= 1000000 {
		t.Fatal("stream was not interrupted")
	}
	if rowsSeen >= 1000000 {
		t.Fatalf("read %d rows despite cancel", rowsSeen)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("cancel took %v", elapsed)
	}
}

func TestLiveStreamLargeResultTruncates(t *testing.T) {
	app := newLivePGApp(t)
	app.settings.ResultLimit = 1000
	var events []StreamEvent
	if err := app.StreamQuery(context.Background(), "c1", "SELECT i FROM generate_series(1,5000) i", "", func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	done := events[len(events)-1]
	if done.Type != "done" || done.RowCount != 1000 || !done.Truncated {
		t.Fatalf("done = %+v", done)
	}
	rows := 0
	batches := 0
	for _, ev := range events {
		if ev.Type == "rows" {
			batches++
			rows += len(ev.Rows)
		}
	}
	if rows != 1000 || batches != 5 {
		t.Fatalf("rows=%d batches=%d", rows, batches)
	}
}
