package pool

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestGetOpensOncePerDatabase(t *testing.T) {
	var mu sync.Mutex
	opens := map[string]int{}
	s := New(func(database string) (*sql.DB, error) {
		mu.Lock()
		opens[database]++
		mu.Unlock()
		return sql.Open("sqlite3", ":memory:")
	}, 4)
	defer s.Close()

	first, err := s.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("the same database must reuse its pool")
	}
	if opens["a"] != 1 {
		t.Fatalf("opened %d times", opens["a"])
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d", s.Len())
	}
}

// More databases than the cap must evict the least-recently-used one.
func TestGetEvictsLeastRecentlyUsed(t *testing.T) {
	s := New(func(database string) (*sql.DB, error) {
		return sql.Open("sqlite3", ":memory:")
	}, 2)
	defer s.Close()

	if _, err := s.Get("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("b"); err != nil {
		t.Fatal(err)
	}
	// Touch a so b becomes the least recently used.
	if _, err := s.Get("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("c"); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("len = %d, want 2", s.Len())
	}
	s.mu.Lock()
	_, hasA := s.pools["a"]
	_, hasB := s.pools["b"]
	_, hasC := s.pools["c"]
	s.mu.Unlock()
	if !hasA || hasC == false || hasB {
		t.Fatalf("cache = a:%v b:%v c:%v, want b evicted", hasA, hasB, hasC)
	}
}

func TestGetPropagatesOpenerError(t *testing.T) {
	s := New(func(string) (*sql.DB, error) {
		return nil, fmt.Errorf("cannot open")
	}, 1)
	defer s.Close()
	if _, err := s.Get("x"); err == nil {
		t.Fatal("the opener's error must surface")
	}
	if s.Len() != 0 {
		t.Fatalf("a failed open must not be cached: %d", s.Len())
	}
}

func TestCloseDropsEverything(t *testing.T) {
	s := New(func(string) (*sql.DB, error) { return sql.Open("sqlite3", ":memory:") }, 2)
	if _, err := s.Get("a"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s.Len() != 0 {
		t.Fatalf("len after Close = %d", s.Len())
	}
	// A closed set can still serve a fresh database (Close is not a tombstone).
	if _, err := s.Get("b"); err != nil {
		t.Fatalf("reopen after Close: %v", err)
	}
	s.Close()
}

// A dial that completes after Close must not repopulate the map: the pools it
// saw were torn down, and a stale dial result would leave a live connection
// that the user believes is closed.
func TestDialLandingAfterCloseIsDropped(t *testing.T) {
	release := make(chan struct{})
	s := New(func(string) (*sql.DB, error) {
		<-release // block until the test releases the dial
		return sql.Open("sqlite3", ":memory:")
	}, 4)
	// Start a dial and let it block.
	done := make(chan struct{})
	go func() {
		_, _ = s.Get("db1")
		close(done)
	}()
	// Give the goroutine time to enter the opener.
	time.Sleep(50 * time.Millisecond)
	// Close while the dial is in flight.
	s.Close()
	// Release the dial.
	close(release)
	// The dial lands under a different closeSeq: its result is dropped.
	<-done
	if s.Len() != 0 {
		t.Fatalf("dialed pool leaked into a closed set: len=%d", s.Len())
	}
}

// N concurrent Gets for one database join a single dial instead of racing
// their own handshakes (the dial is the expensive part: connect + ping).
func TestGetConcurrentSameDatabaseDialsOnce(t *testing.T) {
	var opens atomic.Int64
	s := New(func(database string) (*sql.DB, error) {
		opens.Add(1)
		time.Sleep(50 * time.Millisecond) // a handshake-sized window
		return sql.Open("sqlite3", ":memory:")
	}, 4)
	defer s.Close()

	const n = 12
	dbs := make([]*sql.DB, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dbs[i], errs[i] = s.Get("shared")
		}(i)
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if dbs[i] != dbs[0] {
			t.Fatal("concurrent Gets must hand out the same pool")
		}
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("opens = %d, want 1 (callers share the dial)", got)
	}
}

// The dial runs outside the cache lock: one slow database's open must not
// make other databases' Gets wait behind it (this used to hold the lock
// through connect + ping, freezing a whole connection on one bad server).
func TestGetSlowOpenerDoesNotBlockOtherDatabases(t *testing.T) {
	s := New(func(database string) (*sql.DB, error) {
		if database == "slow" {
			time.Sleep(600 * time.Millisecond)
		}
		return sql.Open("sqlite3", ":memory:")
	}, 8)
	defer s.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := s.Get("slow"); err != nil {
			t.Errorf("slow: %v", err)
		}
	}()

	// Give the slow dial a head start into its (former) lock-held window,
	// then require the fast database to answer while it is still dialing.
	time.Sleep(100 * time.Millisecond)
	fastDone := make(chan struct{})
	go func() {
		defer close(fastDone)
		if _, err := s.Get("fast"); err != nil {
			t.Errorf("fast: %v", err)
		}
	}()
	select {
	case <-fastDone:
		// The fast Get landed while the slow dial was still in flight.
	case <-time.After(2 * time.Second):
		t.Fatal("a slow opener blocked another database's Get")
	}
	<-done
}

// When the global gate is at the limit, a Set with pools must evict its
// oldest pool to make room instead of spinning forever. The eviction closes
// asynchronously, so the gate waits for the global count to drop.
func TestGlobalGateEvictsOldestPool(t *testing.T) {
	oldMax, oldTimeout := GlobalMaxPools, GlobalGateTimeout
	GlobalMaxPools, GlobalGateTimeout = 2, 2*time.Second
	defer func() { GlobalMaxPools, GlobalGateTimeout = oldMax, oldTimeout }()

	s := New(func(database string) (*sql.DB, error) {
		return sql.Open("sqlite3", ":memory:")
	}, 4)
	defer s.Close()

	if _, err := s.Get("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("b"); err != nil {
		t.Fatal(err)
	}
	// Global count is at the limit; the next Get must evict the oldest
	// pool ("a") and dial the new one.
	if _, err := s.Get("c"); err != nil {
		t.Fatalf("Get at the global limit: %v", err)
	}
	s.mu.Lock()
	_, hasA := s.pools["a"]
	_, hasC := s.pools["c"]
	s.mu.Unlock()
	if hasA {
		t.Fatal("the oldest pool must be evicted to make room")
	}
	if !hasC {
		t.Fatal("the new pool must be dialed")
	}
}

// A Set with no pools cannot evict, so it must fail with a bounded error
// rather than spin forever at the global gate.
func TestGlobalGateEmptySetTimesOut(t *testing.T) {
	oldMax, oldTimeout := GlobalMaxPools, GlobalGateTimeout
	GlobalMaxPools, GlobalGateTimeout = 1, 300*time.Millisecond
	defer func() { GlobalMaxPools, GlobalGateTimeout = oldMax, oldTimeout }()

	filler := New(func(string) (*sql.DB, error) {
		return sql.Open("sqlite3", ":memory:")
	}, 4)
	defer filler.Close()
	if _, err := filler.Get("x"); err != nil {
		t.Fatal(err)
	}

	empty := New(func(string) (*sql.DB, error) {
		return sql.Open("sqlite3", ":memory:")
	}, 4)
	defer empty.Close()

	start := time.Now()
	_, err := empty.Get("y")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("an empty Set at the global limit must not succeed")
	}
	if !strings.Contains(err.Error(), "global pool limit") {
		t.Fatalf("err = %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("waited %v, want a bounded wait", elapsed)
	}
}

// The dial gate used to be an unbounded blocking send: a caller parked there
// could not be cancelled by its context, so a cancelled request still paid for
// a full dial. The wait is now a select on ctx (TP-11).
func TestGetContextCancelsDialGateWait(t *testing.T) {
	const slots = 8
	release := make(chan struct{})
	var entered atomic.Int64
	s := New(func(database string) (*sql.DB, error) {
		entered.Add(1)
		<-release
		return sql.Open("sqlite3", ":memory:")
	}, slots)
	defer s.Close()

	var wg sync.WaitGroup
	for i := 0; i < slots; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine uses its own database, so none shares a dial.
			_, _ = s.Get(fmt.Sprintf("db%d", i))
		}(i)
	}
	// Wait until every dial has taken a gate slot and entered the opener.
	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() < slots && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if entered.Load() < slots {
		t.Fatalf("only %d of %d dials started", entered.Load(), slots)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := s.GetContext(ctx, "blocked")
		errCh <- err
	}()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled context did not release the dial gate wait")
	}
	close(release)
	wg.Wait()
}
