package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dbcore/dbcore"
)

// collectStream runs StreamQuery and returns the events it produced.
func collectStream(t *testing.T, app *App, query string) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	err := app.StreamQuery(context.Background(), "c1", "", query, "", func(ev StreamEvent) error {
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

// A statement carrying a LIMIT of its own runs untouched: the configured cap
// must neither cut it nor leave it marked untruncated at a smaller count (the
// buffered path's rule, which the stream path used to contradict).
func TestStreamOwnLimitRunsUntouched(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	for i := 3; i <= 12; i++ {
		q := "INSERT INTO t(id,name) VALUES (" + strconv.Itoa(i) + ",'x')"
		if r := app.ExecuteQuery("c1", q, ""); r.Error != "" {
			t.Fatalf("seed: %s", r.Error)
		}
	}
	app.settings.ResultLimit = 5

	events := collectStream(t, app, "SELECT id FROM t ORDER BY id LIMIT 8")
	done := events[len(events)-1]
	if done.Type != "done" || done.RowCount != 8 || done.Truncated {
		t.Fatalf("done = %+v, want rowCount=8, truncated=false", done)
	}
	rows := 0
	for _, ev := range events {
		rows += len(ev.Rows)
	}
	if rows != 8 {
		t.Fatalf("streamed %d rows, want 8", rows)
	}
}

// ResultLimit=0 means "no cap" to the user, but the buffered path still stops
// materializing at maxMaterializeRows. The stream path used to read and ship the
// whole result set — the cap condition was gated on limit > 0 — so this pins the
// hard bound.
func TestStreamZeroLimitHardBound(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	app.settings.ResultLimit = 0 // explicit no-limit

	const n = maxMaterializeRows + 5
	cte := "WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x<" + strconv.Itoa(n) + ") SELECT x FROM seq"
	events := collectStream(t, app, cte)
	done := events[len(events)-1]
	if done.Type != "done" || done.RowCount != maxMaterializeRows || !done.Truncated {
		t.Fatalf("done = %+v, want rowCount=%d truncated", done, maxMaterializeRows)
	}
	rows := 0
	for _, ev := range events {
		rows += len(ev.Rows)
	}
	if rows != maxMaterializeRows {
		t.Fatalf("streamed %d rows, want %d", rows, maxMaterializeRows)
	}
}

// A statement carrying its own LIMIT above the hard bound is still capped by the
// stream path, the way the buffered path hard-bounds it after materialization.
func TestStreamZeroLimitOwnHugeLimitCapped(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	app.settings.ResultLimit = 0

	const n = maxMaterializeRows + 5
	cte := "WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x<" + strconv.Itoa(n) + ") SELECT x FROM seq LIMIT " + strconv.Itoa(n)
	events := collectStream(t, app, cte)
	done := events[len(events)-1]
	if done.Type != "done" || done.RowCount != maxMaterializeRows || !done.Truncated {
		t.Fatalf("done = %+v, want rowCount=%d truncated", done, maxMaterializeRows)
	}
	rows := 0
	for _, ev := range events {
		rows += len(ev.Rows)
	}
	if rows != maxMaterializeRows {
		t.Fatalf("streamed %d rows, want %d", rows, maxMaterializeRows)
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
	err := app.StreamQuery(ctx, "c1", "", "SELECT i FROM generate_series(1,1000000) i", "", func(ev StreamEvent) error {
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
	if err := app.StreamQuery(context.Background(), "c1", "", "SELECT i FROM generate_series(1,5000) i", "", func(ev StreamEvent) error {
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

// Every exit that crosses an integer cell to the grid must agree on the
// big-int rule: |v| > 2^53 crosses as exact digit text, because a JSON number
// of that size rounds through the browser's res.json() — the grid would
// display AND edit-save a different value, and key predicates built from the
// rounded cell hit the wrong row. The buffered cellValue, the streaming
// streamCell, and the preview runSelectCtx all share jsonSafeInt/jsonSafeUint.
func TestBigIntCrossesAsTextEveryExit(t *testing.T) {
	const big = int64(9007199254740993) // 2^53 + 1

	// jsonSafeInt: the shared helper.
	if got := jsonSafeInt(big).(string); got != "9007199254740993" {
		t.Errorf("jsonSafeInt(2^53+1) = %q, want exact digits", got)
	}
	if got := jsonSafeInt(123).(int64); got != 123 {
		t.Errorf("jsonSafeInt(123) = %v, want int64 123", got)
	}
	if got := jsonSafeInt(-(1 << 53) - 1).(string); got != "-9007199254740993" {
		t.Errorf("jsonSafeInt(-(2^53+1)) = %q", got)
	}
	if got := jsonSafeUint(uint64(big)).(string); got != "9007199254740993" {
		t.Errorf("jsonSafeUint(2^53+1) = %q", got)
	}

	// streamCell: the streaming exit.
	if got := streamCell(big).(string); got != "9007199254740993" {
		t.Errorf("streamCell(2^53+1) = %q", got)
	}
	if got := streamCell(uint64(big)).(string); got != "9007199254740993" {
		t.Errorf("streamCell(u64 2^53+1) = %q", got)
	}
	// Small ints stay numbers (the grid orders them numerically).
	if got := streamCell(int64(42)).(int64); got != 42 {
		t.Errorf("streamCell(42) = %v, want int64 42", got)
	}

	// cellValue: the buffered exit.
	cv := cellValue(dbcore.Value{Kind: dbcore.ValInt, I: big})
	if got := cv.(string); got != "9007199254740993" {
		t.Errorf("cellValue(2^53+1) = %q", got)
	}
}

// Binary bytes that are not valid UTF-8 cannot cross as a JSON string (the
// encoder replaces bad bytes with U+FFFD): the old string(v.B) rendered
// mojibake and a copy/edit round-tripped the corruption. blobCell keeps
// valid-UTF-8 bytes as text (a TEXT column the driver hands over as []byte)
// and renders the rest as a hex preview — display and copy stay faithful.
func TestBlobCellKeepsBinaryFaithful(t *testing.T) {
	// Valid UTF-8 stays text.
	if got := blobCell([]byte("plain text")); got != "plain text" {
		t.Errorf("utf8 blob = %q", got)
	}
	// Invalid UTF-8 becomes a hex preview, not U+FFFD mojibake.
	raw := []byte{0xFF, 0xFE, 0x00, 0x41}
	got := blobCell(raw)
	want := `\xfffe0041`
	if !strings.HasPrefix(got, `\x`) || !strings.Contains(got, want) {
		t.Errorf("binary blob = %q, want a \\x hex preview containing %s", got, want)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("binary blob leaked a replacement character: %q", got)
	}
	// A longer blob truncates the preview and states the full size.
	long := make([]byte, 128)
	for i := range long {
		long[i] = byte(0x80 + i%0x40) // invalid-UTF-8 territory
	}
	got = blobCell(long)
	if !strings.HasSuffix(got, "(128 bytes)") {
		t.Errorf("long blob preview = %q, want a size suffix", got)
	}
	// All three exits agree.
	same := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	if cellValue(dbcore.Value{Kind: dbcore.ValBlob, B: same}).(string) != blobCell(same) {
		t.Error("cellValue and blobCell disagree")
	}
	if streamCell(same).(string) != blobCell(same) {
		t.Error("streamCell and blobCell disagree")
	}
}
