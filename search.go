package main

import (
	"fmt"
	"sort"
	"strings"
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
)

// SearchObjects matches a name fragment against tables and views across every
// connected connection, which is what lets the quick-open box find a table that
// is not open anywhere yet.
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
	for _, conn := range conns {
		if len(found) >= limit {
			break
		}
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
			WHERE ROWNUM <= %d`, strings.ReplaceAll(strings.ToUpper(pattern), "'", "''"), limit)
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
