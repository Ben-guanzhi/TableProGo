package main

import (
	"path/filepath"
	"strings"
	"testing"
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
	if got := likePattern(`a_b%c\d`); got != `%a\_b\%c\\d%` {
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
