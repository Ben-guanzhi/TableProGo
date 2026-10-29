package pool

import (
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestGetOpensOncePerDatabase(t *testing.T) {
	var mu sync.Mutex
	opens := map[string]int{}
	s := New(func(database string) (*sql.DB, error) {
		mu.Lock()
		opens[database]++
		mu.Unlock()
		return sql.Open("sqlite", ":memory:")
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
		return sql.Open("sqlite", ":memory:")
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
	s := New(func(string) (*sql.DB, error) { return sql.Open("sqlite", ":memory:") }, 2)
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

// N concurrent Gets for one database join a single dial instead of racing
// their own handshakes (the dial is the expensive part: connect + ping).
func TestGetConcurrentSameDatabaseDialsOnce(t *testing.T) {
	var opens atomic.Int64
	s := New(func(database string) (*sql.DB, error) {
		opens.Add(1)
		time.Sleep(50 * time.Millisecond) // a handshake-sized window
		return sql.Open("sqlite", ":memory:")
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
		return sql.Open("sqlite", ":memory:")
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
