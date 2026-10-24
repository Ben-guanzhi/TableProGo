package pool

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"

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
