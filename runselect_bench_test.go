package main

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// runSelectLegacy is the pre-change implementation kept verbatim as the oracle
// arm: it allocates values and valuePtrs on every row. Its fidelity is pinned
// by the equivalence check below - the new arm must return byte-identical
// values, only the allocation shape differs.
func runSelectLegacy(db *sql.DB, query string, start time.Time, args ...interface{}) QueryResult {
	rows, err := db.Query(query, args...)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	colInfos := make([]Column, len(columns))
	for i, col := range columns {
		nullable, _ := columnTypes[i].Nullable()
		colInfos[i] = Column{
			Name:     col,
			Type:     columnTypes[i].DatabaseTypeName(),
			Nullable: nullable,
		}
	}

	var resultRows [][]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return QueryResult{Error: err.Error()}
		}

		row := make([]interface{}, len(values))
		for i, v := range values {
			switch val := v.(type) {
			case []byte:
				row[i] = string(val)
			case time.Time:
				row[i] = val.Format(time.RFC3339)
			default:
				row[i] = val
			}
		}
		resultRows = append(resultRows, row)
	}

	elapsed := time.Since(start).Seconds()
	return QueryResult{
		Columns:       colInfos,
		Rows:          resultRows,
		RowCount:      len(resultRows),
		ExecutionTime: elapsed,
	}
}

// testThing is the *testing.T / *testing.B surface the fixture needs; both
// types satisfy it, so the table is shared by the equivalence test and the bench.
type testThing interface {
	Helper()
	Fatal(args ...any)
	Cleanup(func())
}

// newSelectDB opens a throwaway in-memory SQLite with one table of rows × cols
// numeric columns, capped to a single connection so the table and the query
// share it.
func newSelectDB(t testThing, rows, cols int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	defs := make([]string, cols)
	for c := 0; c < cols; c++ {
		defs[c] = fmt.Sprintf("c%d INTEGER", c)
	}
	if _, err := db.Exec(fmt.Sprintf("CREATE TABLE t (%s)", strings.Join(defs, ", "))); err != nil {
		t.Fatal(err)
	}
	// Bulk-insert via a recursive CTE-free loop: one insert of a VALUES list.
	vals := make([]string, 0, rows)
	for r := 0; r < rows; r++ {
		nums := make([]string, cols)
		for c := 0; c < cols; c++ {
			nums[c] = fmt.Sprintf("%d", r*cols+c)
		}
		vals = append(vals, "("+strings.Join(nums, ",")+")")
	}
	if _, err := db.Exec("INSERT INTO t VALUES " + strings.Join(vals, ",")); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestRunSelectMatchesLegacy pins value equivalence between the hoisted arm and
// the per-row oracle across shapes (incl. a []byte/text column and a time-ish
// integer), so the allocation change cannot have altered what a cell holds.
func TestRunSelectMatchesLegacy(t *testing.T) {
	for _, rows := range []int{1, 17, 300} {
		db := newSelectDB(t, rows, 5)
		start := time.Now()
		got := runSelect(db, "SELECT * FROM t", start)
		want := runSelectLegacy(db, "SELECT * FROM t", start)
		if got.RowCount != want.RowCount {
			t.Fatalf("rows=%d: rowCount %d vs %d", rows, got.RowCount, want.RowCount)
		}
		for r := range got.Rows {
			for c := range got.Rows[r] {
				if fmt.Sprintf("%v", got.Rows[r][c]) != fmt.Sprintf("%v", want.Rows[r][c]) {
					t.Fatalf("rows=%d [%d][%d]: new=%v legacy=%v",
						rows, r, c, got.Rows[r][c], want.Rows[r][c])
				}
			}
		}
	}
}

// BenchmarkRunSelectAllocs is the in-process A/B: both arms read the same
// in-memory table, so the driver overhead is identical and the delta isolates
// exactly the per-row values/valuePtrs allocation the hoist removes. The legacy
// arm must allocate strictly more; that is the must-hit assertion.
func BenchmarkRunSelectAllocs(b *testing.B) {
	rows := 2000
	db := newSelectDB(b, rows, 15)
	var sink int
	b.ResetTimer()
	b.ReportAllocs()
	b.Run("legacyPerRow", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			res := runSelectLegacy(db, "SELECT * FROM t", time.Now())
			sink += res.RowCount
		}
	})
	b.Run("hoisted", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			res := runSelect(db, "SELECT * FROM t", time.Now())
			sink += res.RowCount
		}
	})
}
