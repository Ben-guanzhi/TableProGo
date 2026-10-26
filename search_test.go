package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tableprosql "github.com/dbcore/dbcore/sqltext"
)

// sqliteDB opens an empty temporary database and hands back the connection the
// rest of the app would hold for it.
func sqliteDB(t *testing.T, id, name string) *Connection {
	t.Helper()
	path := filepath.Join(t.TempDir(), id+".db")
	db, err := createConnection(ConnectionConfig{Type: "sqlite", Database: path})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Connection{ID: id, Name: name, Type: "sqlite", Database: path, DB: db}
}

func searchApp(conns ...*Connection) *App {
	a := &App{savedConnections: []ConnectionConfig{}}
	a.connections = map[string]*Connection{}
	for _, conn := range conns {
		a.connections[conn.ID] = conn
	}
	return a
}

func createPartsTable(t *testing.T, conn *Connection) {
	t.Helper()
	if _, err := conn.DB.Exec(`CREATE TABLE parts (id integer primary key, name text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
}

// The quick-open search has to actually run its query: a malformed statement
// used to come back as "no matches", because one database failing is tolerated.
func TestSearchFindsTablesByNameFragment(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	createPartsTable(t, conn)

	found, err := searchApp(conn).SearchObjects("part", 50)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("want 1 match, got %d (%+v)", len(found), found)
	}
	match := found[0]
	if match.Table != "parts" || match.Type != "table" || match.ConnID != conn.ID {
		t.Errorf("unexpected match: %+v", match)
	}
	if match.Database != conn.Database {
		t.Errorf("match carries the wrong database: %+v", match)
	}
}

// A name fragment is text, not a pattern: "part_" must not stand in for
// "parts" the way LIKE would read an unescaped underscore.
func TestSearchEscapesWildcardsInTheFragment(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	createPartsTable(t, conn)

	for _, fragment := range []string{"part_", "part%", "part"} {
		found, err := searchApp(conn).SearchObjects(fragment, 50)
		if err != nil {
			t.Fatalf("search %q failed: %v", fragment, err)
		}
		want := 0
		if fragment == "part" {
			want = 1
		}
		if len(found) != want {
			t.Errorf("fragment %q: want %d matches, got %d", fragment, want, len(found))
		}
	}
}

func TestLikePatternEscapesForLike(t *testing.T) {
	// The escaping now lives in sqltext.LikePattern (shared with the kernel's
	// catalog queries); assert the contract through the public helper.
	if got := tableprosql.LikePattern(`a_b%c\d`); got != `%a\_b\%c\\d%` {
		t.Errorf("got %s", got)
	}
}

// An empty result that came from failures has to say so, or the user is told
// there is no such table when the catalog was never read.
func TestSearchReportsWhyNothingWasFound(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	createPartsTable(t, conn)
	broken := sqliteDB(t, "conn_1", "offline")
	if err := broken.DB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	found, err := searchApp(broken).SearchObjects("part", 50)
	if err == nil {
		t.Fatalf("a connection that cannot be read should report an error, got %+v", found)
	}
	if !strings.Contains(err.Error(), "offline") {
		t.Errorf("the error should name the connection, got %v", err)
	}

	// A second connection that does answer makes the search useful, so the
	// partial failure must not turn into an error.
	found, err = searchApp(conn, broken).SearchObjects("part", 50)
	if err != nil {
		t.Errorf("matches from another connection should win, got %v", err)
	}
	if len(found) != 1 {
		t.Errorf("want 1 match, got %d", len(found))
	}
}

// Nothing connected is not a failure, it is simply nothing to search.
func TestSearchWithoutConnectionsIsEmpty(t *testing.T) {
	found, err := searchApp().SearchObjects("part", 50)
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("want no matches, got %+v", found)
	}
	if found == nil {
		t.Errorf("an empty result must marshal as [] not null")
	}
}

// The limit is a cap on work per keystroke, not a way to drop matches that a
// caller asked for.
func TestSearchHonoursTheLimitAndTheDefault(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := conn.DB.Exec("CREATE TABLE " + name + " (id integer primary key)"); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	a := searchApp(conn)

	if found, err := a.SearchObjects("a", 2); err != nil || len(found) != 2 {
		t.Errorf("limit 2: got %d matches, err %v", len(found), err)
	}
	if found, err := a.SearchObjects("a", 0); err != nil || len(found) != 3 {
		t.Errorf("unset limit should fall back to the default: got %d, err %v", len(found), err)
	}
}

// The point of the cache: a run of keystrokes walks each connection's catalog
// once, and only a statement that ran rebuilds it. catalogBuilds is the
// number of walks, not the number of keystrokes.
func TestSearchAnswersFromTheCachedCatalogBetweenChanges(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	createPartsTable(t, conn)
	a := searchApp(conn)

	for _, fragment := range []string{"par", "part", "parts"} {
		if _, err := a.SearchObjects(fragment, 50); err != nil {
			t.Fatalf("search %q: %v", fragment, err)
		}
	}
	if builds := catalogBuildsOf(conn); builds != 1 {
		t.Fatalf("want one catalog walk for three keystrokes, got %d", builds)
	}

	// A statement that ran moves the catalog version: the next keystroke
	// finds the new object and has paid for exactly one more walk.
	if result := a.runQuery(context.Background(), "", conn.ID, "CREATE TABLE spare_parts (id integer primary key)", ""); result.Error != "" {
		t.Fatalf("create: %s", result.Error)
	}
	found, err := a.SearchObjects("spare", 50)
	if err != nil {
		t.Fatalf("search after change: %v", err)
	}
	if len(found) != 1 || found[0].Table != "spare_parts" {
		t.Fatalf("a table created after the first walk must be found, got %+v", found)
	}
	if builds := catalogBuildsOf(conn); builds != 2 {
		t.Fatalf("want one rebuild after the change, got %d builds", builds)
	}
}

// The same fragment written any way matches the same table: inside the cache
// the match is a case-insensitive substring, and every engine's LIKE read
// the fragment that way too.
func TestSearchMatchesWithoutRegardToCase(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	if _, err := conn.DB.Exec(`CREATE TABLE MyTable (id integer primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, variant := range []string{"mytable", "MYTABLE", "MyTa"} {
		found, err := searchApp(conn).SearchObjects(variant, 50)
		if err != nil {
			t.Fatalf("search %q: %v", variant, err)
		}
		if len(found) != 1 {
			t.Errorf("variant %q: want 1 match, got %+v", variant, found)
		}
	}
}

// catalogBuildsOf reports how many catalog walks this connection paid for, which
// is what "answered from memory" means: one walk per mutation, not per keystroke.
func catalogBuildsOf(conn *Connection) int { return conn.catalog.Walks() }

// A key/value connection's objects are its keys; the quick-open box must reach
// them, or a whole Redis connection is invisible to the one search meant to
// span everything (P1-20).
func TestSearchFindsRedisKeys(t *testing.T) {
	a, mr, _ := redisTestApp(t)
	mr.Set("user:1", "x")
	mr.Set("user:2", "y")
	mr.Set("order:42", "z")

	found, err := a.SearchObjects("user", 50)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("matches = %+v, want the two user keys", found)
	}
	for _, m := range found {
		if m.Type != "key" {
			t.Errorf("type = %q, want key (picking it opens the value tab)", m.Type)
		}
		if !strings.Contains(m.Table, "user") {
			t.Errorf("match %q does not contain the fragment", m.Table)
		}
		if m.Database != "0" {
			t.Errorf("database = %q, want the connection's 0", m.Database)
		}
	}

	// A fragment that matches nothing must not invent keys.
	found, err = a.SearchObjects("nothing-like-this", 50)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("unexpected matches: %+v", found)
	}
}
