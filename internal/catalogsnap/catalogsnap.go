// Package catalogsnap caches one connection's whole object list for quick-open.
//
// Walking a catalog costs one round trip per database, and the quick-open box
// rebuilds its answer on every keystroke, so the walk is cached against a
// generation counter: a statement that could have changed a catalog bumps the
// generation, and until the next bump every keystroke is answered from memory.
//
// The cache owns only the policy (when to rebuild, when the result is unusable).
// Reading the catalog stays behind Source, which the client implements with its
// own driver and pool handling — that is the interface this package needed from
// the application, instead of reaching into its connection struct.
package catalogsnap

import (
	"sort"
	"sync"
)

// Match is one catalog hit for the quick-open box: enough to address the object
// the way the rest of the application does, plus what it is.
type Match struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Lower    string `json:"-"` // pre-lowered table name, so a keystroke does not re-fold
	Type     string `json:"type"`
}

// Limits bound how much of a catalog the cache will hold. A database at its
// per-database limit is treated as too large to keep in memory: that connection
// answers the old way, one live LIKE per keystroke.
type Limits struct {
	// PerDatabase caps rows kept per database; reaching it disables the cache
	// for that connection.
	PerDatabase int
	// Total caps rows kept per connection.
	Total int
	// Databases caps how many databases are walked.
	Databases int
	// Concurrency is how many databases are read at once. The round trips are
	// the whole cost, and one slow database used to make every keystroke wait
	// for it in turn; 0 means one at a time.
	Concurrency int
}

// Source supplies what the cache cannot know: the current generation, the
// database list of one connection, and the catalog search of one database.
type Source interface {
	// Generation returns the counter that bumps when a catalog may have changed.
	Generation() uint64
	// Databases lists the databases of one connection.
	Databases(connID string) ([]string, error)
	// Search returns objects of one database whose name matches fragment.
	Search(connID, database, fragment string, limit int) ([]Match, error)
}

// Cache is one connection's object list, rebuilt when the generation moves.
type Cache struct {
	mu    sync.Mutex
	built bool
	gen   uint64
	rows  []Match
	ok    bool
	walks int
}

// Rows returns the cached list and whether it is usable. The first call for a
// generation walks the catalog; the lock is held across the walk so a second
// keystroke waits on the one builder instead of starting a duplicate walk.
func (c *Cache) Rows(src Source, connID string, limits Limits) ([]Match, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	gen := src.Generation()
	if c.built && c.gen == gen {
		return c.rows, c.ok
	}
	c.walks++
	c.gen = gen
	c.built = true
	c.rows, c.ok = walk(src, connID, gen, limits)
	return c.rows, c.ok
}

// Walks reports how many times the catalog was actually read, which is what
// "answered from memory" means: one walk per mutation, not per keystroke.
func (c *Cache) Walks() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.walks
}

// walk reads the connection's catalog once, one database at a time or a few in
// parallel according to limits.Concurrency. A failure anywhere makes the whole
// snapshot unusable (ok=false): a partial list would silently hide objects, and
// the caller then answers from the server and reports failures live.
func walk(src Source, connID string, gen uint64, limits Limits) ([]Match, bool) {
	databases, err := src.Databases(connID)
	if err != nil {
		return nil, false
	}
	sort.Strings(databases)
	if limits.Databases > 0 && len(databases) > limits.Databases {
		databases = databases[:limits.Databases]
	}

	type slot struct {
		hits []Match
		err  error
	}
	slots := make([]slot, len(databases))
	workers := limits.Concurrency
	if workers <= 1 {
		for i, database := range databases {
			slots[i].hits, slots[i].err = src.Search(connID, database, "", limits.PerDatabase)
		}
	} else {
		if workers > len(databases) {
			workers = len(databases)
		}
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
		for i := range databases {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				slots[i].hits, slots[i].err = src.Search(connID, databases[i], "", limits.PerDatabase)
			}(i)
		}
		wg.Wait()
	}

	rows := make([]Match, 0, len(databases))
	for i := range databases {
		if slots[i].err != nil {
			return nil, false
		}
		if limits.PerDatabase > 0 && len(slots[i].hits) >= limits.PerDatabase {
			// Too many objects to keep: this connection keeps searching live.
			return nil, false
		}
		rows = append(rows, slots[i].hits...)
	}
	if limits.Total > 0 && len(rows) > limits.Total {
		return nil, false
	}
	return rows, true
}

// Invalidate drops the cache, for a connection that was reconnected.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.built = false
	c.rows = nil
	c.ok = false
}
