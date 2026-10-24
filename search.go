package main

// Quick-open search. The per-connection catalog cache lives in
// internal/catalogsnap; this file implements its Source (generation, database
// list, per-database search) and the live fallback for connections whose
// catalog is too large to keep.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tableprosql "github.com/dbcore/dbcore/sqltext"
	"tablepro-go/internal/catalogsnap"
)

// ObjectMatch is one catalog hit for the quick-open box.
type ObjectMatch = catalogsnap.Match

// A keystroke triggers a search, so the walk stops well before it can become a
// full catalog dump.
const (
	searchDefaultLimit = 50
	searchMaxLimit     = 200
	searchDatabaseCap  = 32
	// Enough of the reasons to explain an empty result without turning one
	// keystroke into a wall of text when a whole server is unreachable.
	searchFailureCap = 3

	searchSnapshotPerDB   = 2000
	searchSnapshotCap     = 50_000
	searchLoadConcurrency = 8
)

// catalogLimits are the caps the snapshot honours.
func (a *App) catalogLimits() catalogsnap.Limits {
	return catalogsnap.Limits{
		PerDatabase: searchSnapshotPerDB,
		Total:       searchSnapshotCap,
		Databases:   searchDatabaseCap,
		Concurrency: searchLoadConcurrency,
	}
}

// generation implements catalogsnap.Source.
func (a *App) Generation() uint64 { return a.catalogVersion.Load() }

// Databases implements catalogsnap.Source.
func (a *App) Databases(connID string) ([]string, error) { return a.GetDatabases(connID) }

// Search implements catalogsnap.Source: one database's objects whose name
// matches fragment. The empty fragment asks for everything (likePattern("") is
// "%%" on every dialect), which the snapshot walk relies on.
func (a *App) Search(connID, database, fragment string, limit int) ([]catalogsnap.Match, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}
	return searchInDatabase(a, conn, database, fragment, limit)
}

// connSnapshot returns the connection's catalog snapshot, walking the catalog
// once per generation.
func (a *App) connSnapshot(conn *Connection) ([]catalogsnap.Match, bool) {
	return conn.catalog.Rows(a, conn.ID, a.catalogLimits())
}

// SearchObjects matches a name fragment against tables and views across every
// connected connection, which is what lets the quick-open box find a table that
// is not open anywhere yet.
//
// Within one catalog generation the answer comes from the cached object list,
// filtered in memory: matching is a case-insensitive substring test on the table
// name, the same reading the engines' LIKE applied before the cache. A
// connection whose catalog could not be cached answers from the server instead,
// one bounded LIKE per database, as before.
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
	// Sort by ID so results are stable between keystrokes instead of
	// reshuffling with the map's random iteration order.
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
		if rows, ok := a.connSnapshot(conn); ok {
			for _, match := range rows {
				if strings.Contains(match.Lower, lower) {
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

// searchInDatabase runs the kernel's object-search statement for one database.
// The per-engine SQL (placeholder style, ILIKE vs LIKE, TOP vs ROWNUM, and the
// inline-literal forms SQL Server/Oracle need) lives in
// sqltext.SearchObjectsQuery, so the quick-open box and the Fyne client share
// one dialect implementation instead of drifting apart.
func searchInDatabase(a *App, conn *Connection, database, fragment string, limit int) ([]ObjectMatch, error) {
	if limit <= 0 {
		return nil, nil
	}
	db := conn.DB
	if conn.Type == "postgresql" {
		// Cross-database search needs a session bound to that database.
		var err error
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
	}
	query, args, err := tableprosql.SearchObjectsQuery(conn.Type, database, fragment, limit)
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(context.Background(), query, args...)
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
		match.Lower = strings.ToLower(match.Table)
		found = append(found, match)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return found, nil
}
