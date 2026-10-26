package main

// Streaming query results.
//
// /api/execute-stream answers with NDJSON: one JSON object per line, flushed as
// it is produced, so the grid can paint the first rows while the server is still
// reading. The events are:
//
//	{"type":"columns","columns":[...]}
//	{"type":"rows","rows":[[...], ...]}     // repeated, bounded by streamBatchSize
//	{"type":"done","rowCount":N,"truncated":bool,"executionTime":secs}
//	{"type":"error","error":"...","needsConfirmation":bool}
//
// Writes, KV commands and unavailable-catalog statements fall back to the
// buffered path and are emitted as columns + one rows batch + done, so the
// client has a single code path.

import (
	"context"
	"time"

	"github.com/dbcore/dbcore"
)

// streamBatchSize is how many rows go into one NDJSON line. Small enough that
// the first paint is quick, large enough that JSON framing is negligible.
const streamBatchSize = 200

// StreamEvent is one NDJSON line of /api/execute-stream.
type StreamEvent struct {
	Type     string          `json:"type"`
	Columns  []Column        `json:"columns,omitempty"`
	Rows     [][]interface{} `json:"rows,omitempty"`
	RowCount int             `json:"rowCount,omitempty"`
	// Truncated says the row cap ended the stream early.
	Truncated     bool    `json:"truncated,omitempty"`
	ExecutionTime float64 `json:"executionTime,omitempty"`
	Error         string  `json:"error,omitempty"`
	// NeedsConfirmation marks a Safe Mode pause: nothing ran, the UI confirms
	// and re-sends (buffered or streamed) through the confirmed path.
	NeedsConfirmation bool `json:"needsConfirmation,omitempty"`
}

// StreamQuery runs query and hands each event to emit. emit returning an error
// aborts the stream (a client that went away stops the work), and ctx cancels
// the statement as well — the HTTP handler passes r.Context() so a closed
// connection stops the server-side query instead of reading to the end.
func (a *App) StreamQuery(ctx context.Context, connID, query, database string, emit func(StreamEvent) error) error {
	// Safe Mode applies before anything runs; the client sees the same verdict
	// shape as the buffered endpoint.
	if _, err := a.kvConnection(connID); err != nil {
		if guard := guardWrite(a.GetSettings().SafeMode, query); guard.Blocked || guard.NeedsConfirmation {
			return emit(StreamEvent{Type: "error", Error: guard.Reason, NeedsConfirmation: guard.NeedsConfirmation})
		}
	}

	conn, err := a.connection(connID)
	if err != nil {
		return emit(StreamEvent{Type: "error", Error: err.Error()})
	}
	if !dbcore.IsReadQuery(query) {
		return a.streamBuffered(connID, query, database, emit)
	}

	runCtx, done := a.beginRunWith(ctx, connID)
	defer done()
	start := time.Now()

	db, err := a.queryPool(conn, database)
	if err != nil {
		return emit(StreamEvent{Type: "error", Error: err.Error()})
	}
	limit := a.GetSettings().ResultLimit
	fetch := limit
	if limit > 0 {
		fetch = limit + 1 // one extra row tells us the result was cut
	}
	probe := withRowLimit(query, conn.Type, fetch)

	rows, err := db.QueryContext(runCtx, probe)
	if err != nil {
		return a.finishStream(connID, database, query, start, err, 0, false, emit)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return a.finishStream(connID, database, query, start, err, 0, false, emit)
	}
	columnTypes, _ := rows.ColumnTypes()
	colInfos := make([]Column, len(columns))
	for i, col := range columns {
		var nullable bool
		var typeName string
		if columnTypes != nil && i < len(columnTypes) {
			nullable, _ = columnTypes[i].Nullable()
			typeName = columnTypes[i].DatabaseTypeName()
		}
		colInfos[i] = Column{
			Name:      col,
			Type:      typeName,
			Nullable:  nullable,
			IsDecimal: dbcore.IsExactDecimalType(typeName),
		}
	}
	if err := emit(StreamEvent{Type: "columns", Columns: colInfos}); err != nil {
		return err
	}

	values := make([]interface{}, len(columns))
	ptrs := make([]interface{}, len(columns))
	for i := range values {
		ptrs[i] = &values[i]
	}

	batch := make([][]interface{}, 0, streamBatchSize)
	emitted := 0
	truncated := false
	for rows.Next() {
		if runCtx.Err() != nil {
			return a.finishStream(connID, database, query, start, runCtx.Err(), emitted, false, emit)
		}
		if err := rows.Scan(ptrs...); err != nil {
			return a.finishStream(connID, database, query, start, err, emitted, false, emit)
		}
		if limit > 0 && emitted+len(batch) >= limit {
			// The extra row is a probe only when the cap is ours; stop here and
			// mark the result as cut.
			truncated = probe != query
			break
		}
		row := make([]interface{}, len(values))
		for i, v := range values {
			row[i] = streamCell(v)
		}
		batch = append(batch, row)
		if len(batch) == streamBatchSize {
			if err := emit(StreamEvent{Type: "rows", Rows: batch}); err != nil {
				return err
			}
			emitted += len(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := emit(StreamEvent{Type: "rows", Rows: batch}); err != nil {
			return err
		}
		emitted += len(batch)
	}
	if err := rows.Err(); err != nil {
		return a.finishStream(connID, database, query, start, err, emitted, truncated, emit)
	}
	return a.finishStream(connID, database, query, start, nil, emitted, truncated, emit)
}

// streamBuffered sends a non-streamable statement (write, KV command) through
// the buffered path as one columns + one rows + done sequence.
func (a *App) streamBuffered(connID, query, database string, emit func(StreamEvent) error) error {
	result := a.ExecuteQuery(connID, query, database)
	if len(result.Columns) > 0 {
		if err := emit(StreamEvent{Type: "columns", Columns: result.Columns}); err != nil {
			return err
		}
	}
	if len(result.Rows) > 0 {
		if err := emit(StreamEvent{Type: "rows", Rows: result.Rows}); err != nil {
			return err
		}
	}
	return emit(StreamEvent{
		Type: "done", RowCount: result.RowCount, Truncated: result.Truncated,
		ExecutionTime: result.ExecutionTime, Error: result.Error,
	})
}

// finishStream emits the terminal event and records history, so a streamed
// statement shows up in the history list exactly like a buffered one.
func (a *App) finishStream(connID, database, query string, start time.Time, err error, rowCount int, truncated bool, emit func(StreamEvent) error) error {
	event := StreamEvent{
		Type: "done", RowCount: rowCount, Truncated: truncated,
		ExecutionTime: time.Since(start).Seconds(),
	}
	if err != nil {
		event.Error = err.Error()
	}
	a.recordStatement(connID, database, query, QueryResult{
		RowCount: rowCount, Truncated: truncated, Error: event.Error,
		ExecutionTime: event.ExecutionTime,
	})
	if emitErr := emit(event); emitErr != nil {
		return emitErr
	}
	return err
}

// streamCell maps one scanned value onto the JSON-friendly cell the grid uses
// (same rules as cellValue, plus the []byte case database/sql hands back).
func streamCell(v interface{}) interface{} {
	switch val := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(val)
	case time.Time:
		return val.Format(time.RFC3339)
	default:
		return val
	}
}
