package rowsync

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// ---- builders ----

func vInt(n int64) Value    { return Value{Kind: ValInt, I: n} }
func vText(s string) Value  { return Value{Kind: ValText, S: s} }
func vNull() Value          { return Value{Kind: ValNull} }

func row(key int64, cols map[string]Value) Row {
	return Row{Key: []Value{vInt(key)}, Cols: cols}
}

func row2(k1, k2 int64, cols map[string]Value) Row {
	return Row{Key: []Value{vInt(k1), vInt(k2)}, Cols: cols}
}

func cols(pairs ...string) map[string]Value {
	m := map[string]Value{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] == "NULL" {
			m[pairs[i]] = vNull()
		} else {
			m[pairs[i]] = vText(pairs[i+1])
		}
	}
	return m
}

// summarize is the test shorthand: "op key" lines in merge order.
func summarize(changes []Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		switch c.Op {
		case OpInsert:
			out = append(out, "insert "+c.Key[0].String())
		case OpDelete:
			out = append(out, "delete "+c.Key[0].String())
		case OpUpdate:
			var cols []string
			for name := range c.Values {
				cols = append(cols, name)
			}
			out = append(out, "update "+c.Key[0].String()+"("+strings.Join(sortedNames(c.Values), ",")+")")
		}
	}
	return out
}

func sortedNames(m map[string]Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// ---- key/value comparison ----

func TestCompareValuesNulls(t *testing.T) {
	if !valuesEqual(vNull(), vNull()) {
		t.Fatal("NULL equals NULL for sync purposes")
	}
	if valuesEqual(vNull(), vText("")) {
		t.Fatal("NULL is not an empty string")
	}
	if valuesEqual(vNull(), vInt(0)) {
		t.Fatal("NULL is not zero")
	}
	// NULL orders before everything, which is what positions it in the merge.
	if c := compareValues(vNull(), vInt(1)); c >= 0 {
		t.Fatalf("null should order before a value, got %d", c)
	}
}

func TestCompareValuesTextIsExact(t *testing.T) {
	if valuesEqual(vText("1.50"), vText("1.5")) {
		t.Fatal("text comparison is byte-exact: 1.50 and 1.5 differ")
	}
	if valuesEqual(vText(""), vNull()) {
		t.Fatal("empty text is not NULL")
	}
}

func TestCompareKeysTupleOrder(t *testing.T) {
	// A composite key orders position by position.
	if c, _ := compareKeys([]Value{vInt(1), vInt(2)}, []Value{vInt(1), vInt(3)}); c >= 0 {
		t.Fatalf("second position decides: %d", c)
	}
	if c, _ := compareKeys([]Value{vInt(2), vInt(9)}, []Value{vInt(1), vInt(1)}); c <= 0 {
		t.Fatalf("first position dominates: %d", c)
	}
	if c, _ := compareKeys([]Value{vInt(1), vInt(2)}, []Value{vInt(1), vInt(2)}); c != 0 {
		t.Fatalf("equal keys: %d", c)
	}
}

func TestCompareKeysLengthMismatchIsAnError(t *testing.T) {
	if _, err := compareKeys([]Value{vInt(1)}, []Value{vInt(1), vInt(2)}); err == nil {
		t.Fatal("a length mismatch must be reported, not guessed at")
	}
}

func TestCompareNumbersAcrossKinds(t *testing.T) {
	dec := Value{Kind: ValDecimal, S: "1.5"}
	flt := Value{Kind: ValFloat, F: 1.5}
	if !valuesEqual(dec, flt) {
		t.Fatal("decimal 1.5 equals float 1.5")
	}
	if !valuesEqual(vInt(2), Value{Kind: ValDecimal, S: "2"}) {
		t.Fatal("int 2 equals decimal 2")
	}
}

// ---- the merge ----

func TestDiffRowsIdenticalIsEmpty(t *testing.T) {
	src := []Row{row(1, cols("name", "a")), row(2, cols("name", "b"))}
	dst := []Row{row(1, cols("name", "a")), row(2, cols("name", "b"))}
	changes, err := DiffRows(src, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("identical tables must produce no changes: %+v", changes)
	}
}

func TestDiffRowsInsertsDeletesUpdates(t *testing.T) {
	src := []Row{
		row(1, cols("name", "a")),
		row(2, cols("name", "CHANGED")),
		row(4, cols("name", "new")),
	}
	dst := []Row{
		row(1, cols("name", "a")),
		row(2, cols("name", "old")),
		row(3, cols("name", "gone")),
	}
	changes, err := DiffRows(src, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := summarize(changes)
	want := []string{"update 2(name)", "delete 3", "insert 4"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
	// The update carries only the differing column.
	if changes[0].Values["name"].S != "CHANGED" {
		t.Fatalf("update values = %+v", changes[0].Values)
	}
}

func TestDiffRowsSequencesAreStable(t *testing.T) {
	src := []Row{row(1, cols("a", "x")), row(5, cols("a", "y"))}
	dst := []Row{row(3, cols("a", "z"))}
	changes, err := DiffRows(src, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range changes {
		if c.Seq != i+1 {
			t.Fatalf("change %d has seq %d", i, c.Seq)
		}
	}
}

func TestDiffRowsEmptySides(t *testing.T) {
	// An empty source deletes everything; an empty target inserts everything.
	all, _ := DiffRows([]Row{row(1, cols("a", "x"))}, nil, Options{})
	if len(all) != 1 || all[0].Op != OpInsert {
		t.Fatalf("empty target = %+v", all)
	}
	none, _ := DiffRows(nil, []Row{row(1, cols("a", "x"))}, Options{})
	if len(none) != 1 || none[0].Op != OpDelete {
		t.Fatalf("empty source = %+v", none)
	}
	both, _ := DiffRows(nil, nil, Options{})
	if len(both) != 0 {
		t.Fatalf("both empty = %+v", both)
	}
}

func TestDiffRowsNULLHandling(t *testing.T) {
	// NULL on both sides is agreement; NULL 鈫?value is an update; value 鈫?NULL too.
	src := []Row{
		row(1, cols("v", "NULL")),
		row(2, cols("v", "set")),
		row(3, cols("v", "NULL")),
	}
	dst := []Row{
		row(1, cols("v", "NULL")),
		row(2, cols("v", "NULL")),
		row(3, cols("v", "was")),
	}
	changes, err := DiffRows(src, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want exactly the two real differences", changes)
	}
	if changes[0].Op != OpUpdate || changes[0].Key[0].I != 2 {
		t.Fatalf("first change = %+v", changes[0])
	}
	if changes[1].Op != OpUpdate || changes[1].Key[0].I != 3 {
		t.Fatalf("second change = %+v", changes[1])
	}
}

func TestDiffRowsIgnoreColumns(t *testing.T) {
	src := []Row{row(1, map[string]Value{"name": vText("same"), "touched": vText("2026-01-01")})}
	dst := []Row{row(1, map[string]Value{"name": vText("same"), "touched": vText("2026-06-01")})}
	changes, err := DiffRows(src, dst, Options{IgnoreColumns: map[string]bool{"touched": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("an ignored column cannot trigger an update: %+v", changes)
	}
}

func TestDiffRowsIgnoreDoesNotHideInsert(t *testing.T) {
	// An ignored column is still copied on insert: a new row needs every column.
	src := []Row{row(1, map[string]Value{"name": vText("n"), "touched": vText("x")})}
	dst := []Row{}
	changes, err := DiffRows(src, dst, Options{IgnoreColumns: map[string]bool{"touched": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Values["touched"].S != "x" {
		t.Fatalf("insert must carry every column: %+v", changes)
	}
}

func TestDiffRowsCompositeKey(t *testing.T) {
	src := []Row{
		row2(1, 1, cols("v", "a")),
		row2(1, 2, cols("v", "b")),
		row2(2, 1, cols("v", "c")),
	}
	dst := []Row{
		row2(1, 1, cols("v", "a")),
		row2(2, 1, cols("v", "CHANGED")),
	}
	changes, err := DiffRows(src, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := summarize(changes)
	// (1,1) matches with equal values (no change); (1,2) is source-only;
	// (2,1) differs in v.
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0] != "insert 1" || got[1] != "update 2(v)" {
		t.Fatalf("got %v", got)
	}
}

func TestDiffRowsRefusesUnorderedStream(t *testing.T) {
	// A stream that goes backwards would make the merge silently wrong; it must
	// be refused loudly instead.
	unordered := []Row{row(2, cols("a", "x")), row(1, cols("a", "y"))}
	ordered := []Row{row(1, cols("a", "x"))}
	_, err := DiffRows(unordered, ordered, Options{})
	if err == nil {
		t.Fatal("an unordered stream must be refused")
	}
	if !errors.Is(err, ErrNotOrdered) {
		t.Fatalf("error = %v, want ErrNotOrdered", err)
	}
}

func TestDiffRowsPropagatesFetcherErrors(t *testing.T) {
	boom := errors.New("boom")
	err := DiffStream(
		func() ([]Row, error) { return nil, boom },
		func() ([]Row, error) { return nil, io.EOF },
		Options{},
		func(Change) (bool, error) { return true, nil },
	)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
}

func TestDiffStreamEarlyStop(t *testing.T) {
	// A preview that only wants the first change stops the merge; the fetcher
	// is not drained.
	src := []Row{row(1, cols("a", "x")), row(2, cols("a", "y")), row(3, cols("a", "z"))}
	dst := []Row{}
	count := 0
	err := DiffStream(
		batchFetcher(src, 2),
		batchFetcher(dst, 2),
		Options{},
		func(Change) (bool, error) {
			count++
			return false, nil // stop after the first
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("emit called %d times, want 1", count)
	}
}
// batchFetcher returns rows in batches of n, so the merger's cross-batch
// boundary is exercised (a batch-1 stream never tests that the merger carries
// its position across a pull).
func batchFetcher(rows []Row, n int) func() ([]Row, error) {
	pos := 0
	return func() ([]Row, error) {
		if pos >= len(rows) {
			return nil, nil
		}
		end := pos + n
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[pos:end]
		pos = end
		return batch, nil
	}
}

func TestDiffStreamBatchesMatchRowByRow(t *testing.T) {
	// The same input split into different batch sizes must produce the same
	// plan: the merge is a function of the stream's content, not of how the
	// caller chunked it.
	src := []Row{row(1, cols("a", "1")), row(2, cols("a", "2")), row(3, cols("a", "3")), row(4, cols("a", "4"))}
	dst := []Row{row(2, cols("a", "2")), row(4, cols("a", "4"))}

	collect := func(n int) []string {
		var out []string
		si := batchFetcher(src, n)
		di := batchFetcher(dst, n)
		if err := DiffStream(si, di, Options{}, func(c Change) (bool, error) {
			out = append(out, string(c.Op)+" "+c.Key[0].String())
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := collect(1)
	for _, n := range []int{2, 3, 10} {
		again := collect(n)
		if len(first) != len(again) {
			t.Fatalf("batch size %d changed the plan: %v vs %v", n, first, again)
		}
		for i := range first {
			if first[i] != again[i] {
				t.Fatalf("batch size %d changed line %d: %q vs %q", n, i, first[i], again[i])
			}
		}
	}
}

func TestDiffStreamMultiBatchPlan(t *testing.T) {
	// Batches of 2 over a 3-row source and 1-row target: the plan must cover
	// every key even though the streams cross batch boundaries mid-merge.
	// src(2) and dst(2) hold the same value, so key 2 produces no change.
	src := []Row{row(1, cols("a", "1")), row(2, cols("a", "2")), row(3, cols("a", "3"))}
	dst := []Row{row(2, cols("a", "2"))}
	var out []string
	err := DiffStream(
		batchFetcher(src, 2),
		batchFetcher(dst, 2),
		Options{},
		func(c Change) (bool, error) {
			out = append(out, string(c.Op)+" "+c.Key[0].String())
			return true, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"insert 1", "insert 3"}
	if len(out) != len(want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q", i, out[i], want[i])
		}
	}
}

