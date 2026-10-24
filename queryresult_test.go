package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dbcore/dbcore"
)

// The grid consumes the kernel's typed cells, so the JSON-facing conversion is
// pinned here: an int stays a number, a blob stays text and a timestamp keeps
// the RFC3339 form the frontend parses.
func TestQueryResultFromRendersKernelValues(t *testing.T) {
	when := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	res := &dbcore.QueryResult{
		Columns: []dbcore.ColumnInfo{
			{Name: "id", Type: "INTEGER", Nullable: false},
			{Name: "name", Type: "TEXT", Nullable: true},
			{Name: "blob", Type: "BLOB"},
			{Name: "missing", Type: "TEXT"},
		},
		Rows: [][]dbcore.Value{{
			{Kind: dbcore.ValInt, I: 7},
			{Kind: dbcore.ValText, S: "a"},
			{Kind: dbcore.ValBlob, B: []byte("xy")},
			{Kind: dbcore.ValNull},
		}, {
			{Kind: dbcore.ValFloat, F: 1.5},
			{Kind: dbcore.ValBool, Bool: true},
			{Kind: dbcore.ValTime, T: when},
			{Kind: dbcore.ValInt, I: -1},
		}},
		AffectedRows: 3,
		LastInsertID: 9,
	}

	got := queryResultFrom(res, nil)
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	if got.RowCount != 2 || len(got.Rows) != 2 {
		t.Fatalf("rows: count %d, %d rows", got.RowCount, len(got.Rows))
	}
	if got.AffectedRows != 3 || got.LastInsertID != 9 {
		t.Errorf("counters: affected %d, last id %d", got.AffectedRows, got.LastInsertID)
	}
	wantCols := []Column{
		{Name: "id", Type: "INTEGER", Nullable: false},
		{Name: "name", Type: "TEXT", Nullable: true},
		{Name: "blob", Type: "BLOB"},
		{Name: "missing", Type: "TEXT"},
	}
	if !reflect.DeepEqual(got.Columns, wantCols) {
		t.Errorf("columns: %+v", got.Columns)
	}

	wantRows := [][]interface{}{
		{int64(7), "a", "xy", nil},
		{1.5, true, when.Format(time.RFC3339), int64(-1)},
	}
	if !reflect.DeepEqual(got.Rows, wantRows) {
		t.Errorf("rows: %#v", got.Rows)
	}
}

func TestQueryResultFromSurfacesDriverErrors(t *testing.T) {
	got := queryResultFrom(nil, errors.New("boom"))
	if got.Error != "boom" {
		t.Fatalf("error: %q", got.Error)
	}
}
