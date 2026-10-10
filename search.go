package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ObjectMatch is one catalog hit for the quick-open box: enough to address the
// object the way the rest of the app does, plus what it is.
type ObjectMatch struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Type     string `json:"type"`
}

// A keystroke triggers a search, so the walk stops well before it can become a
// full catalog dump.
const (
	searchDefaultLimit = 50
	searchMaxLimit     = 200
	searchDatabaseCap  = 32
	// Enough of the reasons to explain an empty result without turning one
	// keystroke into a wall of text when a whole server is unreachable.
	searchFailureCap = 3

	// A snapshot holds up to searchSnapshotPerDB rows per database and
	// searchSnapshotCap rows per connection. A database at its per-database
	// limit is treated as a catalog too large to keep in memory: that
	// connection answers the old way, one live LIKE per keystroke, exactly as
	// before the cache existed.
	searchSnapshotPerDB   = 2000
	searchSnapshotCap     = 50_000
	searchLoadConcurrency = 8
)

// catalogSnapshot is one connection's whole object list, cached against the
// catalog generation it was built at. App.catalogVersion bumps whenever a
// statement that could change a catalog succeeds (main.go runQuery), so a
// keystroke between two bumps is answered from memory instead of from the
// server: O(connections) catalog queries for the keystroke that triggers the
// rebuild, and zero for every keystroke after it.
//
// built distinguishes "never walked" from a walk that produced nothing. ok is
// false when the walk hit a database it could not read or a catalog too large
// to keep; the caller then answers from the server per keystroke (the old
// behaviour), which also keeps reporting failures as they happen.
//
// A table created outside this app stays invisible to quick-open until the
// catalog is next walked: the version has no way to hear about someone else's
// DDL. Within the app every mutation lands on the bumping path.
type catalogSnapshot struct {
	built bool
	gen   uint64
	rows  []ObjectMatch
	ok    bool
}

// connSnapshot returns the connection's catalog snapshot, walking the catalog
// once per generation. The lock is held across the walk so a second keystroke
// waits on the one builder instead of starting a duplicate walk.
func (a *App) connSnapshot(conn *Connection) catalogSnapshot {
	conn.catalogMu.Lock()
	defer conn.catalogMu.Unlock()

	gen := a.catalogVersion.Load()
	if conn.catalog.built && conn.catalog.gen == gen {
		return conn.catalog
	}
	conn.catalogBuilds++
	conn.catalog = a.buildCatalog(conn)
	return conn.catalog
}

// buildCatalog walks one connection's catalog: the database list, then every
// database concurrently (the server round-trips are the whole cost, and one
// slow database used to make every keystroke wait for it in turn). The result
// is in the same order the live search would have produced it: databases
// sorted by name, objects in the order the engine returns them.
//
// The empty fragment asks each engine for everything it has: likePattern("")
// is "%%", which every dialect treats as a match-all, so the listing needs no
// query of its own.
func (a *App) buildCatalog(conn *Connection) catalogSnapshot {
	snap := catalogSnapshot{built: true, gen: a.catalogVersion.Load()}

	databases, err := a.GetDatabases(conn.ID)
	if err != nil {
		// Nothing can be cached about a connection that cannot be listed; the
		// live path will report this failure on the keystroke that asks.
		return snap
	}
	sort.Strings(databases)
	if len(databases) > searchDatabaseCap {
		databases = databases[:searchDatabaseCap]
	}

	type slot struct {
		rows []ObjectMatch
		err  error
	}
	slots := make([]slot, len(databases))
	var wg sync.WaitGroup
	sem := make(chan struct{}, searchLoadConcurrency)
	for i, database := range databases {
		wg.Add(1)
		go func(i int, database string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows, err := searchInDatabase(a, conn, database, "", searchSnapshotPerDB)
			slots[i].rows, slots[i].err = rows, err
		}(i, database)
	}
	wg.Wait()

	for i := range databases {
		if slots[i].err != nil {
			return catalogSnapshot{built: true, gen: snap.gen}
		}
		if len(slots[i].rows) >= searchSnapshotPerDB {
			// Too many objects to keep: this connection keeps searching live.
			return catalogSnapshot{built: true, gen: snap.gen}
		}
		snap.rows = append(snap.rows, slots[i].rows...)
	}
	if len(snap.rows) > searchSnapshotCap {
		return catalogSnapshot{built: true, gen: snap.gen}
	}
	snap.ok = true
	return snap
}

// SearchObjects matches a name fragment against tables and views across every
// connected connection, which is what lets the quick-open box find a table that
// is not open anywhere yet.
//
// Within one catalog generation the answer comes from the cached object list,
// filtered in memory: matching is a case-insensitive substring test on the
// table name, the same reading the engines' LIKE applied before the cache —
// where it is case-insensitive, and where a fragment is literal text rather
// than a pattern (search_test.go pins that). A connection whose catalog could
// not be cached answers from the server instead, one bounded LIKE per
// database, as before.
//
// A failure in one database is not reported as long as another one answered,
// because a partial list is more useful than an error. When nothing came back
// and something did fail, that failure is returned: "no matches" and "the
// catalog could not be read" look the same in the UI but are not the same thing.
func (a *App) SearchObjects(pattern string, limit int) ([]ObjectMatch, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return []ObjectMatch{}, nil
	}
	if limit <= 0 || limit > searchMaxLimit {
		limit = searchDefaultLimit
	}

	a.connectionsMu.RLock()
	conns := make([]*Connection, 0, len(a.connections))
	for _, conn := range a.connections {
		if conn.DB != nil {
			conns = append(conns, conn)
		}
	}
	a.connectionsMu.RUnlock()
	// Connection ids are index-based, so sorting them keeps results stable
	// between keystrokes instead of reshuffling with the map.
	sort.Slice(conns, func(i, j int) bool { return conns[i].ID < conns[j].ID })

	found := []ObjectMatch{}
	var failures []string
	record := func(scope string, err error) {
		if err == nil {
			return
		}
		if len(failures) < searchFailureCap {
			failures = append(failures, scope+": "+err.Error())
		}
	}
	lower := strings.ToLower(pattern)
	for _, conn := range conns {
		if len(found) >= limit {
			break
		}
		if snap := a.connSnapshot(conn); snap.ok {
			for _, match := range snap.rows {
				if strings.Contains(strings.ToLower(match.Table), lower) {
					found = append(found, match)
					if len(found) >= limit {
						return found, nil
					}
				}
			}
			continue
		}

		// No usable snapshot: the pre-cache path, reporting failures live.
		databases, err := a.GetDatabases(conn.ID)
		if err != nil {
			record(conn.Name, err)
			continue
		}
		sort.Strings(databases)
		if len(databases) > searchDatabaseCap {
			databases = databases[:searchDatabaseCap]
		}
		for _, database := range databases {
			if len(found) >= limit {
				break
			}
			hits, err := searchInDatabase(a, conn, database, pattern, limit-len(found))
			if err != nil {
				// One database being unreachable - offline, no rights - must not
				// hide the matches the others have.
				record(conn.Name+"."+database, err)
				continue
			}
			found = append(found, hits...)
		}
	}
	if len(found) == 0 && len(failures) > 0 {
		return found, fmt.Errorf("catalog search failed: %s", strings.Join(failures, "; "))
	}
	return found, nil
}

// likePattern wraps the fragment for a LIKE test, escaping the wildcards the
// user typed so "a_b" cannot stand for any three characters.
func likePattern(fragment string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(fragment)
	return "%" + escaped + "%"
}

func searchInDatabase(a *App, conn *Connection, database, fragment string, limit int) ([]ObjectMatch, error) {
	if limit <= 0 {
		return nil, nil
	}
	pattern := likePattern(fragment)

	db := conn.DB
	var query string
	var args []interface{}
	switch conn.Type {
	case "mysql":
		query = fmt.Sprintf(`SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME LIKE ? ORDER BY TABLE_NAME LIMIT %d`, limit)
		args = []interface{}{database, pattern}
	case "postgresql":
		var err error
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
		query = fmt.Sprintf(`SELECT n.nspname, c.relname, CASE WHEN c.relkind = 'v' THEN 'VIEW' ELSE 'TABLE' END
			FROM pg_class c
			INNER JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN ('r', 'v', 'm', 'p')
				AND n.nspname NOT IN ('pg_catalog', 'information_schema')
				AND c.relname ILIKE $1
			ORDER BY 1, 2 LIMIT %d`, limit)
		args = []interface{}{pattern}
	case "sqlite":
		// The literal 'sqlite_%' needs its percent doubled: this whole string is a
		// Sprintf format, and a bare % would corrupt the query.
		query = fmt.Sprintf(`SELECT '', name, UPPER(type) FROM sqlite_master
			WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%%' AND name LIKE ?
			ORDER BY name LIMIT %d`, limit)
		args = []interface{}{pattern}
	case "sqlserver":
		name, err := quoteIdent(conn.Type, database)
		if err != nil {
			return nil, err
		}
		// go-mssqldb leaves "?" markers untouched in some statements, so the
		// pattern goes in as an escaped literal like the other catalog reads.
		query = fmt.Sprintf(`SELECT TOP (%d) TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE
			FROM %s.INFORMATION_SCHEMA.TABLES
			WHERE TABLE_NAME LIKE %s ESCAPE '\' ORDER BY TABLE_NAME`,
			limit, name, quotedLiteral("sqlserver", pattern))
	case "oracle":
		query = fmt.Sprintf(`SELECT * FROM (
			SELECT OWNER, OBJECT_NAME, OBJECT_TYPE FROM ALL_OBJECTS
			WHERE OBJECT_TYPE IN ('TABLE', 'VIEW') AND UPPER(OBJECT_NAME) LIKE '%s' ESCAPE '\'
			ORDER BY OWNER, OBJECT_NAME)
			WHERE ROWNUM <= %d`, quotedLiteral("oracle", strings.ToUpper(pattern)), limit)
	default:
		return nil, fmt.Errorf("unsupported type: %s", conn.Type)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var found []ObjectMatch
	for rows.Next() {
		var match ObjectMatch
		var objectType string
		if err := rows.Scan(&match.Schema, &match.Table, &objectType); err != nil {
			return nil, err
		}
		match.ConnID = conn.ID
		match.Database = database
		match.Type = normalizeTableType(objectType)
		found = append(found, match)
	}
	return found, nil
}
