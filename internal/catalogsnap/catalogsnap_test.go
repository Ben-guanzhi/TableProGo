package catalogsnap

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	gen       uint64
	genByConn map[string]uint64
	dbs       []string
	hits      map[string][]Match
	errDB     error
	errS      error
	calls     int
	// block, when non-nil, holds the Search call until it is closed. It lets a
	// test hold a walk in flight long enough for concurrent callers to arrive.
	block chan struct{}
}

func (f *fakeSource) Generation(connID string) uint64 {
	if f.genByConn != nil {
		if g, ok := f.genByConn[connID]; ok {
			return g
		}
	}
	return f.gen
}

func (f *fakeSource) Databases(string) ([]string, error) {
	if f.errDB != nil {
		return nil, f.errDB
	}
	return f.dbs, nil
}

func (f *fakeSource) Search(connID, database, fragment string, limit int) ([]Match, error) {
	f.calls++
	if f.block != nil {
		<-f.block
	}
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
	// The database list itself capped: a partial snapshot is not a complete
	// one, so ok=false (the caller falls back to live search).
	src = &fakeSource{gen: 1, dbs: []string{"a", "b", "c", "d"}, hits: map[string][]Match{"a": {{Table: "t"}}}}
	if _, ok := (&Cache{}).Rows(src, "c1", limits(100, 100, 1)); ok {
		t.Fatal("a capped database list must disable the snapshot, not serve a partial one as complete")
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

// A TTL forces a rebuild even when the generation did not move: the server's
// schema can change independently of the client's DDL statements, and the
// cache must not serve stale metadata past the TTL.
func TestRowsRebuildsAfterTTL(t *testing.T) {
	src := &fakeSource{
		gen:  1,
		dbs:  []string{"a"},
		hits: map[string][]Match{"a": {{Table: "t1"}}},
	}
	c := &Cache{}
	// First call builds the cache.
	rows, ok := c.Rows(src, "c1", Limits{PerDatabase: 100, TTL: time.Hour})
	if !ok || len(rows) != 1 {
		t.Fatalf("rows=%v ok=%v", rows, ok)
	}
	before := src.calls
	// Same generation, TTL not expired: no rebuild.
	rows, ok = c.Rows(src, "c1", Limits{PerDatabase: 100, TTL: time.Hour})
	if !ok || len(rows) != 1 {
		t.Fatalf("cached rows=%v ok=%v", rows, ok)
	}
	if src.calls != before {
		t.Fatalf("walked %d times, want no rebuild", src.calls)
	}
	// TTL expired: rebuild even though the generation did not move.
	// Use time.Millisecond (not Nanosecond) because Windows' clock resolution
	// is coarse enough that time.Since(builtAt) can be 0 for rapid calls.
	// The source's hits change BEFORE the expired call, so the rebuild picks
	// up the new state.
	src.hits = map[string][]Match{"a": {{Table: "t2"}}}
	time.Sleep(2 * time.Millisecond)
	rows, ok = c.Rows(src, "c1", Limits{PerDatabase: 100, TTL: time.Millisecond})
	if !ok || len(rows) != 1 {
		t.Fatalf("rebuilt rows=%v ok=%v", rows, ok)
	}
	if src.calls != before+1 {
		t.Fatalf("walked %d times, want one more after TTL expiry", src.calls)
	}
	// The rebuilt rows reflect the source's current state.
	if rows[0].Table != "t2" {
		t.Fatalf("rows after TTL = %v, want t2", rows)
	}
}

// A zero TTL means no TTL check: the cache is only rebuilt on generation moves.
func TestRowsZeroTTLNoRebuild(t *testing.T) {
	src := &fakeSource{
		gen:  1,
		dbs:  []string{"a"},
		hits: map[string][]Match{"a": {{Table: "t1"}}},
	}
	c := &Cache{}
	rows, ok := c.Rows(src, "c1", Limits{PerDatabase: 100})
	if !ok || len(rows) != 1 {
		t.Fatalf("rows=%v ok=%v", rows, ok)
	}
	before := src.calls
	// Zero TTL: no rebuild regardless of time.
	rows, ok = c.Rows(src, "c1", Limits{PerDatabase: 100})
	if !ok || len(rows) != 1 {
		t.Fatalf("cached rows=%v ok=%v", rows, ok)
	}
	if src.calls != before {
		t.Fatalf("walked %d times, want no rebuild with zero TTL", src.calls)
	}
}

// The generation is scoped to a connection: a bump on connection B must not
// rebuild connection A's snapshot. A global counter did exactly that — one
// connection's DDL invalidated another's quick-open cache (TP-21).
func TestRowsGenerationIsPerConnection(t *testing.T) {
	src := &fakeSource{
		genByConn: map[string]uint64{"a": 1, "b": 1},
		dbs:       []string{"d"},
		hits:      map[string][]Match{"d": {{Table: "t"}}},
	}
	c := &Cache{}
	if _, ok := c.Rows(src, "a", limits(10, 10, 10)); !ok {
		t.Fatal("conn a first walk failed")
	}
	before := src.calls
	// Bump only connection b's generation: conn a's snapshot is unaffected.
	src.genByConn["b"] = 2
	if _, ok := c.Rows(src, "a", limits(10, 10, 10)); !ok {
		t.Fatal("conn a cached walk failed")
	}
	if src.calls != before {
		t.Fatalf("another connection's bump rebuilt this snapshot (%d walks)", src.calls-before)
	}
	// Bump this connection's own generation: it rebuilds.
	src.genByConn["a"] = 2
	if _, ok := c.Rows(src, "a", limits(10, 10, 10)); !ok {
		t.Fatal("conn a walk after own bump failed")
	}
	if src.calls != before+1 {
		t.Fatalf("own bump walked %d times, want one", src.calls-before)
	}
}

// A walk in flight must not hold the cache lock: a keystroke arriving mid-walk
// joins the walk instead of being serialized behind it. Otherwise one slow
// database freezes the quick-open box for every keystroke during the walk.
func TestRowsConcurrentCallersShareOneWalk(t *testing.T) {
	release := make(chan struct{})
	src := &fakeSource{
		gen:   1,
		dbs:   []string{"a"},
		hits:  map[string][]Match{"a": {{Table: "t1"}}},
		block: release,
	}
	c := &Cache{}

	const callers = 8
	var wg sync.WaitGroup
	started := make(chan struct{}, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			c.Rows(src, "c1", limits(10, 10, 10))
		}()
	}
	// Wait until every caller is inside Rows before releasing the walk.
	for i := 0; i < callers; i++ {
		<-started
	}
	close(release)
	wg.Wait()
	if src.calls != 1 {
		t.Fatalf("walked %d times, want one shared walk", src.calls)
	}
}
