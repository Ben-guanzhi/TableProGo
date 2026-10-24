package catalogsnap

import (
	"fmt"
	"testing"
)

type fakeSource struct {
	gen   uint64
	dbs   []string
	hits  map[string][]Match
	errDB error
	errS  error
	calls int
}

func (f *fakeSource) Generation() uint64 { return f.gen }

func (f *fakeSource) Databases(string) ([]string, error) {
	if f.errDB != nil {
		return nil, f.errDB
	}
	return f.dbs, nil
}

func (f *fakeSource) Search(connID, database, fragment string, limit int) ([]Match, error) {
	f.calls++
	if f.errS != nil {
		return nil, f.errS
	}
	return f.hits[database], nil
}

func limits(perDB, total, dbs int) Limits {
	return Limits{PerDatabase: perDB, Total: total, Databases: dbs}
}

func TestRowsWalksOncePerGeneration(t *testing.T) {
	src := &fakeSource{
		gen:  1,
		dbs:  []string{"b", "a"},
		hits: map[string][]Match{"a": {{Table: "t1"}}, "b": {{Table: "t2"}}},
	}
	c := &Cache{}
	rows, ok := c.Rows(src, "c1", limits(100, 1000, 10))
	if !ok || len(rows) != 2 {
		t.Fatalf("rows=%v ok=%v", rows, ok)
	}
	// Databases are walked in sorted order, so the list is stable across keystrokes.
	if rows[0].Table != "t1" || rows[1].Table != "t2" {
		t.Fatalf("order = %v", rows)
	}
	// A second call at the same generation must not walk again.
	src.dbs = nil
	rows, ok = c.Rows(src, "c1", limits(100, 1000, 10))
	if !ok || len(rows) != 2 {
		t.Fatalf("cached rows=%v ok=%v", rows, ok)
	}
	// A bump rebuilds.
	src.gen = 2
	src.dbs = []string{"a"}
	src.hits = map[string][]Match{"a": {{Table: "t3"}}}
	rows, ok = c.Rows(src, "c1", limits(100, 1000, 10))
	if !ok || len(rows) != 1 || rows[0].Table != "t3" {
		t.Fatalf("after bump rows=%v ok=%v", rows, ok)
	}
}

func TestRowsReportsUnusableSnapshots(t *testing.T) {
	// A database that cannot be listed disables the cache for this generation.
	src := &fakeSource{gen: 1, errDB: fmt.Errorf("offline")}
	if _, ok := (&Cache{}).Rows(src, "c1", limits(10, 10, 10)); ok {
		t.Fatal("a failed database list must not look like an empty catalog")
	}
	// A failed search does the same.
	src = &fakeSource{gen: 1, dbs: []string{"a"}, errS: fmt.Errorf("no rights")}
	if _, ok := (&Cache{}).Rows(src, "c1", limits(10, 10, 10)); ok {
		t.Fatal("a failed search must disable the snapshot")
	}
	// A database whose hit count reaches the per-database cap is too large to keep.
	src = &fakeSource{gen: 1, dbs: []string{"a"}, hits: map[string][]Match{"a": make([]Match, 5)}}
	if _, ok := (&Cache{}).Rows(src, "c1", limits(5, 100, 10)); ok {
		t.Fatal("a capped database must disable the snapshot")
	}
	// Too many rows overall also disables it.
	src = &fakeSource{gen: 1, dbs: []string{"a", "b"}, hits: map[string][]Match{
		"a": make([]Match, 3), "b": make([]Match, 3),
	}}
	if _, ok := (&Cache{}).Rows(src, "c1", limits(100, 5, 10)); ok {
		t.Fatal("a catalog larger than the total cap must disable the snapshot")
	}
	// The database list itself is capped, not an error.
	src = &fakeSource{gen: 1, dbs: []string{"a", "b", "c", "d"}, hits: map[string][]Match{"a": {{Table: "t"}}}}
	rows, ok := (&Cache{}).Rows(src, "c1", limits(100, 100, 1))
	if !ok || len(rows) != 1 {
		t.Fatalf("database cap rows=%v ok=%v", rows, ok)
	}
}

func TestInvalidateForcesWalk(t *testing.T) {
	src := &fakeSource{gen: 1, dbs: []string{"a"}, hits: map[string][]Match{"a": {{Table: "t"}}}}
	c := &Cache{}
	if _, ok := c.Rows(src, "c1", limits(10, 10, 10)); !ok {
		t.Fatal("first walk failed")
	}
	before := src.calls
	c.Invalidate()
	if _, ok := c.Rows(src, "c1", limits(10, 10, 10)); !ok {
		t.Fatal("second walk failed")
	}
	if src.calls != before+1 {
		t.Fatalf("walked %d times, want one more than %d", src.calls, before)
	}
}
