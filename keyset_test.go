package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func seekKeysOf(dbType string, defs ...ColumnInfo) []keyColumn {
	return keysetKeys(dbType, defs, nil)
}

// Only whole numbers and text come back out of the grid the way they went in,
// so those are the key types a page may be seeked by. Everything else has to
// keep using OFFSET, and a type that merely looks like a number must not slip
// through the gate.
func TestSeekableKeyTypeGate(t *testing.T) {
	integers := []string{"INTEGER", "int", "BIGINT", "smallint", "INT8", "SERIAL", "BIGSERIAL", "ROWID", "bigint(20) unsigned"}
	for _, colType := range integers {
		if kind := seekableKeyType(colType); kind != keyInteger {
			t.Errorf("%s: want an integer key, got %v", colType, kind)
		}
	}
	texts := []string{"TEXT", "varchar(255)", "CHAR(8)", "NVARCHAR(16)", "VARCHAR2(30)", "CLOB", "longtext"}
	for _, colType := range texts {
		if kind := seekableKeyType(colType); kind != keyText {
			t.Errorf("%s: want a text key, got %v", colType, kind)
		}
	}
	// POINT ends in "INT" and DECIMAL is a number, but neither can be seeked by:
	// the first is not a key the grid round-trips, the second prints differently
	// than it is stored.
	refused := []string{"POINT", "MULTIPOINT", "REAL", "FLOAT", "DOUBLE", "DECIMAL(10,2)", "NUMERIC",
		"DATE", "DATETIME", "TIMESTAMP", "BLOB", "BYTEA", "UUID", "JSON", "NUMBER", "INTERVAL DAY TO SECOND", ""}
	for _, colType := range refused {
		if kind := seekableKeyType(colType); kind != keyNotSeekable {
			t.Errorf("%q: want no key, got %v", colType, kind)
		}
	}
}

func TestKeysetKeysRefusesEnginesItCannotBind(t *testing.T) {
	keyed := []ColumnInfo{{Name: "id", Type: "INTEGER", IsPrimaryKey: true}}
	for _, dbType := range []string{"sqlite", "mysql", "postgresql"} {
		if len(seekKeysOf(dbType, keyed...)) != 1 {
			t.Errorf("%s: want one seekable key", dbType)
		}
	}
	for _, dbType := range []string{"sqlserver", "oracle", "redis"} {
		if keys := seekKeysOf(dbType, keyed...); keys != nil {
			t.Errorf("%s: want no key at all, got %v", dbType, keys)
		}
	}
	// One unseekable column is enough to give up on the whole key.
	mixed := []ColumnInfo{
		{Name: "a", Type: "INTEGER", IsPrimaryKey: true},
		{Name: "b", Type: "DATE", IsPrimaryKey: true},
	}
	if keys := seekKeysOf("sqlite", mixed...); keys != nil {
		t.Errorf("mixed key: want none, got %v", keys)
	}
	if keys := seekKeysOf("sqlite", ColumnInfo{Name: "a", Type: "INTEGER"}); keys != nil {
		t.Errorf("keyless object: want none, got %v", keys)
	}
	if keys := keysetKeys("sqlite", keyed, fmt.Errorf("catalog unreadable")); keys != nil {
		t.Errorf("unreadable catalog: want none, got %v", keys)
	}
}

func TestKeysetWhereShapesTheSeek(t *testing.T) {
	keys := seekKeysOf("sqlite",
		ColumnInfo{Name: "region", Type: "TEXT", IsPrimaryKey: true},
		ColumnInfo{Name: "id", Type: "INTEGER", IsPrimaryKey: true})

	forward, args, err := keysetWhere("sqlite", keys, []string{"east", "41"}, false)
	if err != nil {
		t.Fatalf("forward seek: %v", err)
	}
	if want := `("region" > ? OR ("region" = ? AND "id" > ?))`; forward != want {
		t.Errorf("forward SQL:\n got %s\nwant %s", forward, want)
	}
	if len(args) != 3 || args[0] != "east" || args[1] != "east" || args[2] != int64(41) {
		t.Errorf("forward args: %v", args)
	}

	backward, _, err := keysetWhere("sqlite", keys, []string{"east", "41"}, true)
	if err != nil {
		t.Fatalf("backward seek: %v", err)
	}
	if want := `("region" < ? OR ("region" = ? AND "id" < ?))`; backward != want {
		t.Errorf("backward SQL:\n got %s\nwant %s", backward, want)
	}

	// PostgreSQL numbers its parameters, one per mention, because the expansion
	// cannot name the same value twice.
	pgKeys := seekKeysOf("postgresql",
		ColumnInfo{Name: "region", Type: "TEXT", IsPrimaryKey: true},
		ColumnInfo{Name: "id", Type: "INTEGER", IsPrimaryKey: true})
	pg, pgArgs, err := keysetWhere("postgresql", pgKeys, []string{"east", "41"}, false)
	if err != nil {
		t.Fatalf("postgres seek: %v", err)
	}
	if want := `("region" > $1 OR ("region" = $2 AND "id" > $3))`; pg != want {
		t.Errorf("postgres SQL:\n got %s\nwant %s", pg, want)
	}
	if len(pgArgs) != 3 {
		t.Errorf("postgres args: %v", pgArgs)
	}

	// A key above the signed range is a real thing (unsigned bigint), and it must
	// stay the exact digits the page was built from.
	single := seekKeysOf("mysql", ColumnInfo{Name: "id", Type: "BIGINT", IsPrimaryKey: true})
	one, oneArgs, err := keysetWhere("mysql", single, []string{"9007199254740993"}, false)
	if err != nil {
		t.Fatalf("64-bit seek: %v", err)
	}
	if want := "(`id` > ?)"; one != want {
		t.Errorf("mysql SQL: got %s want %s", one, want)
	}
	if len(oneArgs) != 1 || oneArgs[0] != int64(9007199254740993) {
		t.Errorf("a signed key must stay exact, got %v (%T)", oneArgs, oneArgs[0])
	}
	above, aboveArgs, err := keysetWhere("mysql", single, []string{"18446744073709551615"}, false)
	if err != nil {
		t.Fatalf("unsigned seek: %v", err)
	}
	if len(aboveArgs) != 1 || aboveArgs[0] != uint64(18446744073709551615) || !strings.Contains(above, "?") {
		t.Errorf("a key above the signed range must stay exact, got %v (%T) in %s", aboveArgs, aboveArgs, above)
	}
}

func TestKeysetWhereRefusesCursorsItCannotTrust(t *testing.T) {
	number := seekKeysOf("sqlite", ColumnInfo{Name: "id", Type: "INTEGER", IsPrimaryKey: true})
	text := seekKeysOf("sqlite", ColumnInfo{Name: "tag", Type: "TEXT", IsPrimaryKey: true})

	cases := []struct {
		name   string
		keys   []keyColumn
		cursor []string
	}{
		{"wrong length", number, []string{}},
		{"too many values", number, []string{"1", "2"}},
		{"not a number", number, []string{"1; DROP TABLE items"}},
		{"a decimal where a whole number is declared", number, []string{"1.5"}},
		{"a value past 64 bits", number, []string{"99999999999999999999999"}},
		{"a nul byte in text", text, []string{"a\x00b"}},
	}
	for _, tc := range cases {
		sql, args, err := keysetWhere("sqlite", tc.keys, tc.cursor, false)
		if err == nil {
			t.Errorf("%s: want a refusal, got %s with %v", tc.name, sql, args)
		}
	}
}

// pageIDs reads the first column of every row, which is what a page's identity
// is made of in these fixtures.
func pageIDs(t *testing.T, result QueryResult) string {
	t.Helper()
	if result.Error != "" {
		t.Fatalf("preview failed: %s", result.Error)
	}
	parts := make([]string, len(result.Rows))
	for i, row := range result.Rows {
		parts[i] = fmt.Sprintf("%v", row[0])
	}
	return strings.Join(parts, ",")
}

func mustExec(t *testing.T, conn *Connection, statement string, args ...interface{}) {
	t.Helper()
	if _, err := conn.DB.Exec(statement, args...); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// A page sought by key has to hold the very rows the same page reached by
// counting, or the cursor is worth nothing.
func TestCursorWalkMatchesOffsetWalk(t *testing.T) {
	conn := sqliteDB(t, "conn_keyset", "scratch")
	mustExec(t, conn, `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`)
	for i := 1; i <= 10; i++ {
		mustExec(t, conn, `INSERT INTO items (id, name) VALUES (?, ?)`, i, fmt.Sprint(i))
	}
	a := searchApp(conn)

	limit := 3
	var byCursor []string
	after := []string(nil)
	for page := 1; page <= 4; page++ {
		result := a.previewPage(conn.ID, conn.Database, "", "items", limit, (page-1)*limit, nil, after, nil)
		byCursor = append(byCursor, pageIDs(t, result))
		if len(result.EndCursor) != 1 || len(result.StartCursor) != 1 {
			t.Fatalf("page %d cursors: %v / %v", page, result.StartCursor, result.EndCursor)
		}
		after = result.EndCursor
	}

	var byOffset []string
	for page := 1; page <= 4; page++ {
		byOffset = append(byOffset, pageIDs(t, a.Preview(conn.ID, conn.Database, "", "items", limit, (page-1)*limit, nil)))
	}

	want := []string{"1,2,3", "4,5,6", "7,8,9", "10"}
	if strings.Join(byCursor, "|") != strings.Join(want, "|") {
		t.Errorf("cursor walk: %v", byCursor)
	}
	if strings.Join(byCursor, "|") != strings.Join(byOffset, "|") {
		t.Errorf("cursor walk %v differs from offset walk %v", byCursor, byOffset)
	}
}

// 上一页 is the other half of the walk, and it reads backwards from the page on
// screen: the rows have to come back in the order the grid shows them.
func TestBackwardSeekReturnsThePreviousPage(t *testing.T) {
	conn := sqliteDB(t, "conn_keyset_back", "scratch")
	mustExec(t, conn, `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`)
	for i := 1; i <= 10; i++ {
		mustExec(t, conn, `INSERT INTO items (id, name) VALUES (?, ?)`, i, fmt.Sprint(i))
	}
	a := searchApp(conn)

	first := a.previewPage(conn.ID, conn.Database, "", "items", 3, 0, nil, nil, nil)
	page2 := a.previewPage(conn.ID, conn.Database, "", "items", 3, 3, nil, first.EndCursor, nil)
	page3 := a.previewPage(conn.ID, conn.Database, "", "items", 3, 6, nil, page2.EndCursor, nil)
	if got := pageIDs(t, page3); got != "7,8,9" {
		t.Fatalf("page 3: %s", got)
	}

	backTo2 := a.previewPage(conn.ID, conn.Database, "", "items", 3, 3, nil, nil, page3.StartCursor)
	if got, want := pageIDs(t, backTo2), "4,5,6"; got != want {
		t.Errorf("previous page: got %s want %s", got, want)
	}
	if strings.Join(backTo2.StartCursor, ",") != strings.Join(page2.StartCursor, ",") {
		t.Errorf("cursors after stepping back: %v vs %v", backTo2.StartCursor, page2.StartCursor)
	}
	if strings.Join(backTo2.EndCursor, ",") != strings.Join(page2.EndCursor, ",") {
		t.Errorf("cursors after stepping back: %v vs %v", backTo2.EndCursor, page2.EndCursor)
	}
	backTo1 := a.previewPage(conn.ID, conn.Database, "", "items", 3, 0, nil, nil, backTo2.StartCursor)
	if got := pageIDs(t, backTo1); got != "1,2,3" {
		t.Errorf("two steps back: %s", got)
	}
}

func TestTextKeySeekSurvivesAwkwardValues(t *testing.T) {
	conn := sqliteDB(t, "conn_keyset_text", "scratch")
	mustExec(t, conn, `CREATE TABLE tags (tag TEXT PRIMARY KEY, hits INTEGER)`)
	for _, label := range []string{"O'Brien", `back\slash`, "100%", `a"b`, "plain", "tilde～", "tab\there"} {
		mustExec(t, conn, `INSERT INTO tags (tag, hits) VALUES (?, ?)`, label, 1)
	}
	a := searchApp(conn)

	var byCursor []string
	after := []string(nil)
	for page := 1; page <= 4; page++ {
		result := a.previewPage(conn.ID, conn.Database, "", "tags", 2, (page-1)*2, nil, after, nil)
		if len(result.Rows) == 0 {
			break
		}
		byCursor = append(byCursor, pageIDs(t, result))
		if len(result.EndCursor) != 1 {
			t.Fatalf("page %d: no text cursor", page)
		}
		after = result.EndCursor
	}

	var byOffset []string
	for page := 1; page <= 4; page++ {
		result := a.Preview(conn.ID, conn.Database, "", "tags", 2, (page-1)*2, nil)
		if len(result.Rows) == 0 {
			break
		}
		byOffset = append(byOffset, pageIDs(t, result))
	}
	if len(byCursor) != 4 || strings.Join(byCursor, "|") != strings.Join(byOffset, "|") {
		t.Errorf("text key: cursor walk %v differs from offset walk %v", byCursor, byOffset)
	}
	// The awkward values are the point: a quote, a backslash or a tab in a key
	// must not end up on a page twice, which is what a mis-rendered cursor does.
	seen := map[string]bool{}
	for _, page := range byCursor {
		for _, tag := range strings.Split(page, ",") {
			if seen[tag] {
				t.Errorf("tag %q shown on more than one page: %v", tag, byCursor)
			}
			seen[tag] = true
		}
	}
	if len(seen) != 7 {
		t.Errorf("text walk covered %d rows over %d pages, want all 7", len(seen), len(byCursor))
	}
}

func TestCompositeKeySeekMatchesOffset(t *testing.T) {
	conn := sqliteDB(t, "conn_keyset_composite", "scratch")
	mustExec(t, conn, `CREATE TABLE comp (region TEXT, id INTEGER, label TEXT, PRIMARY KEY (region, id))`)
	for _, region := range []string{"east", "west"} {
		for i := 1; i <= 5; i++ {
			mustExec(t, conn, `INSERT INTO comp (region, id, label) VALUES (?, ?, ?)`, region, i, region)
		}
	}
	a := searchApp(conn)

	var byCursor []string
	after := []string(nil)
	for page := 1; page <= 6; page++ {
		result := a.previewPage(conn.ID, conn.Database, "", "comp", 2, (page-1)*2, nil, after, nil)
		if len(result.Rows) == 0 {
			break
		}
		byCursor = append(byCursor, pageIDs(t, result))
		if len(result.EndCursor) != 2 {
			t.Fatalf("page %d: composite cursor should name both key columns, got %v", page, result.EndCursor)
		}
		after = result.EndCursor
	}

	var byOffset []string
	for page := 1; page <= 6; page++ {
		result := a.Preview(conn.ID, conn.Database, "", "comp", 2, (page-1)*2, nil)
		if len(result.Rows) == 0 {
			break
		}
		byOffset = append(byOffset, pageIDs(t, result))
	}
	if len(byCursor) != len(byOffset) {
		t.Fatalf("cursor walk has %d pages, offset walk %d", len(byCursor), len(byOffset))
	}
	for i := range byCursor {
		if byCursor[i] != byOffset[i] {
			t.Errorf("page %d: cursor %s, offset %s", i+1, byCursor[i], byOffset[i])
		}
	}

	// And one step back over a composite key.
	third := a.previewPage(conn.ID, conn.Database, "", "comp", 2, 4, nil, nil, nil)
	back := a.previewPage(conn.ID, conn.Database, "", "comp", 2, 2, nil, nil, third.StartCursor)
	if pageIDs(t, back) != byOffset[1] {
		t.Errorf("composite step back: %s want %s", pageIDs(t, back), byOffset[1])
	}
}

// An object the program cannot seek by has to page exactly as it did before the
// cursor existed: same rows, no cursor, and a cursor sent by a client that
// changed its mind is ignored rather than obeyed.
func TestKeylessAndUnseekablePagesFallBackToOffset(t *testing.T) {
	conn := sqliteDB(t, "conn_keyset_fallback", "scratch")
	mustExec(t, conn, `CREATE TABLE keyless (note TEXT)`)
	mustExec(t, conn, `CREATE TABLE realkey (v REAL PRIMARY KEY, n TEXT)`)
	mustExec(t, conn, `CREATE TABLE datekey (d DATE PRIMARY KEY, n TEXT)`)
	mustExec(t, conn, `CREATE TABLE keyed (id INTEGER PRIMARY KEY, n TEXT)`)
	for i := 1; i <= 6; i++ {
		mustExec(t, conn, `INSERT INTO keyless (note) VALUES (?)`, i)
		mustExec(t, conn, `INSERT INTO realkey (v, n) VALUES (?, ?)`, float64(i), i)
		mustExec(t, conn, `INSERT INTO datekey (d, n) VALUES (?, ?)`, fmt.Sprintf("2026-01-0%d", i), i)
		mustExec(t, conn, `INSERT INTO keyed (id, n) VALUES (?, ?)`, i, i)
	}
	a := searchApp(conn)

	for _, table := range []string{"keyless", "realkey", "datekey"} {
		result := a.previewPage(conn.ID, conn.Database, "", table, 2, 2, nil, []string{"anything"}, nil)
		if result.Error != "" {
			t.Fatalf("%s: %s", table, result.Error)
		}
		if result.StartCursor != nil || result.EndCursor != nil {
			t.Errorf("%s: a page with no key must not hand back a cursor, got %v / %v",
				table, result.StartCursor, result.EndCursor)
		}
		byOffset := a.Preview(conn.ID, conn.Database, "", table, 2, 2, nil)
		if len(result.Rows) != len(byOffset.Rows) {
			t.Errorf("%s: an ignored cursor changed the size of the page: %d rows vs %d",
				table, len(result.Rows), len(byOffset.Rows))
		}
	}

	// A key the walk could use, but a cursor of the wrong shape: the OFFSET page
	// is still the right answer, and it hands back a cursor of its own.
	broken := a.previewPage(conn.ID, conn.Database, "", "keyed", 2, 2, nil, []string{"not a number"}, nil)
	if broken.Error != "" {
		t.Fatalf("keyed with a broken cursor: %s", broken.Error)
	}
	if got, want := pageIDs(t, broken), "3,4"; got != want {
		t.Errorf("broken cursor fell back wrong: %s want %s", got, want)
	}
	if strings.Join(broken.EndCursor, ",") != "4" {
		t.Errorf("a fallback page should still hand back a cursor, got %v", broken.EndCursor)
	}
}

// The filter bar narrows the page, the cursor steps through it: both conditions
// have to hold at once, or a filtered walk repeats rows.
func TestCursorSeeksInsideAFilteredPage(t *testing.T) {
	conn := sqliteDB(t, "conn_keyset_filter", "scratch")
	mustExec(t, conn, `CREATE TABLE items (id INTEGER PRIMARY KEY, kind TEXT)`)
	for i := 1; i <= 9; i++ {
		kind := "odd"
		if i%2 == 0 {
			kind = "even"
		}
		mustExec(t, conn, `INSERT INTO items (id, kind) VALUES (?, ?)`, i, kind)
	}
	a := searchApp(conn)
	filters := []TableFilter{{Column: "kind", Op: filterEq, Value: "even"}}
	preview := func(offset int, after, before []string) QueryResult {
		return a.previewPage(conn.ID, conn.Database, "", "items", 2, offset, filters, after, before)
	}

	var byCursor []string
	after := []string(nil)
	for page := 1; page <= 3; page++ {
		result := preview((page-1)*2, after, nil)
		byCursor = append(byCursor, pageIDs(t, result))
		after = result.EndCursor
	}
	var byOffset []string
	for page := 1; page <= 3; page++ {
		byOffset = append(byOffset, pageIDs(t, a.Preview(conn.ID, conn.Database, "", "items", 2, (page-1)*2, filters)))
	}
	if strings.Join(byCursor, "|") != "2,4|6,8|" {
		t.Errorf("filtered cursor walk: %v", byCursor)
	}
	if strings.Join(byCursor, "|") != strings.Join(byOffset, "|") {
		t.Errorf("filtered walk: cursor %v differs from offset %v", byCursor, byOffset)
	}

	back := preview(0, nil, []string{"6"})
	if pageIDs(t, back) != "2,4" {
		t.Errorf("filtered step back: %s", pageIDs(t, back))
	}
}

// The key columns are found by name in the result, not by position, so a page
// keeps its cursor whatever the projection looks like.
func TestPageCursorsFollowTheResultColumns(t *testing.T) {
	keys := seekKeysOf("sqlite", ColumnInfo{Name: "id", Type: "INTEGER", IsPrimaryKey: true})
	page := [][]interface{}{{"bolt", int64(7)}, {"gear", int64(9)}}
	columns := []Column{{Name: "name"}, {Name: "ID"}}
	start, end := pageCursors(page, keys, columns)
	if strings.Join(start, ",") != "7" || strings.Join(end, ",") != "9" {
		t.Errorf("cursors: %v / %v", start, end)
	}

	// A projection without the key cannot be seeked from.
	if start, end := pageCursors(page, keys, []Column{{Name: "name"}}); start != nil || end != nil {
		t.Errorf("want no cursor without the key column, got %v / %v", start, end)
	}
	if start, end := pageCursors(nil, keys, columns); start != nil || end != nil {
		t.Errorf("an empty page seeks nowhere, got %v / %v", start, end)
	}
	// A value that is not the declared type is not guessed at.
	if start, end := pageCursors([][]interface{}{{"bolt", "seven"}}, keys, columns); start != nil || end != nil {
		t.Errorf("want no cursor for a non-numeric integer key, got %v / %v", start, end)
	}
}

// A composite key in SQLite is numbered, not flagged: table_info answers 1..n
// for its columns, and reading that as "is this the first one" left every later
// key column out of the page's ordering and out of the key rows are written
// back by.
func TestGetColumnsMarksEveryKeyColumn(t *testing.T) {
	conn := sqliteDB(t, "conn_keyset_pk", "scratch")
	mustExec(t, conn, `CREATE TABLE comp (id INTEGER, region TEXT, label TEXT, PRIMARY KEY (region, id))`)
	a := searchApp(conn)

	cols, err := a.GetColumns(conn.ID, conn.Database, "", "comp")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	keyed := map[string]bool{}
	for _, col := range cols {
		if col.IsPrimaryKey {
			keyed[strings.ToLower(col.Name)] = true
		}
	}
	if len(keyed) != 2 || !keyed["region"] || !keyed["id"] {
		t.Errorf("want region and id as the key, got %v", keyed)
	}
	if orderBy := pageOrderFrom("sqlite", cols, nil); !strings.Contains(orderBy, `"region"`) ||
		!strings.Contains(orderBy, `"id"`) {
		t.Errorf("the page is not ordered by the whole key: %s", orderBy)
	}
	if keys := keysetKeys("sqlite", cols, nil); len(keys) != 2 {
		t.Errorf("want a two column seek, got %d columns", len(keys))
	}
}

// The data card and the server agree on nothing but these field names, so the
// trip through JSON is the one place a cursor can be dropped without anything
// reporting it. This drives the real endpoint the frontend calls.
func TestPreviewWireCarriesTheCursor(t *testing.T) {
	conn := sqliteDB(t, "conn_keyset_wire", "scratch")
	mustExec(t, conn, `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`)
	for i := 1; i <= 6; i++ {
		mustExec(t, conn, `INSERT INTO items (id, name) VALUES (?, ?)`, i, fmt.Sprint(i))
	}
	a := searchApp(conn)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	a.startHTTPServerOnListener(listener)
	url := "http://" + listener.Addr().String() + "/api/preview"

	send := func(request previewRequest) QueryResult {
		t.Helper()
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		response, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var page QueryResult
		if err := json.Unmarshal(payload, &page); err != nil {
			t.Fatalf("decode %s: %v", payload, err)
		}
		if page.Error != "" {
			t.Fatalf("the page came back with an error: %s", page.Error)
		}
		return page
	}
	base := func() previewRequest {
		return previewRequest{ConnID: conn.ID, Database: conn.Database, Table: "items", Limit: 2}
	}

	first := send(base())
	if got := pageIDs(t, first); got != "1,2" {
		t.Fatalf("first page: %s", got)
	}
	if strings.Join(first.EndCursor, ",") != "2" || strings.Join(first.StartCursor, ",") != "1" {
		t.Fatalf("first page edges: %v / %v", first.StartCursor, first.EndCursor)
	}

	nextRequest := base()
	nextRequest.Offset = 2
	nextRequest.After = first.EndCursor
	second := send(nextRequest)
	if got := pageIDs(t, second); got != "3,4" {
		t.Errorf("page after the cursor: %s", got)
	}
	if len(second.StartCursor) != 1 {
		t.Fatalf("second page edges: %v", second.StartCursor)
	}

	// 上一页 steps back from the second page's own edge, and a request without a
	// cursor is still read by counting rows.
	stepBack := base()
	stepBack.Before = second.StartCursor
	if got := pageIDs(t, send(stepBack)); got != "1,2" {
		t.Errorf("page before the cursor: %s", got)
	}
	if got := pageIDs(t, send(base())); got != "1,2" {
		t.Errorf("page by row count: %s", got)
	}
	// A page of an object with no key answers without cursors, so the card keeps
	// counting rows and nothing is mistaken for a key.
	mustExec(t, conn, `CREATE TABLE notes (note TEXT)`)
	mustExec(t, conn, `INSERT INTO notes (note) VALUES ('a'), ('b')`)
	keylessRequest := base()
	keylessRequest.Table = "notes"
	keyless := send(keylessRequest)
	if len(keyless.Rows) != 2 {
		t.Fatalf("keyless page: %d rows", len(keyless.Rows))
	}
	if keyless.StartCursor != nil || keyless.EndCursor != nil {
		t.Errorf("keyless page should carry no cursor, got %v / %v", keyless.StartCursor, keyless.EndCursor)
	}
}

func TestReversePage(t *testing.T) {
	page := [][]interface{}{{int64(1)}, {int64(2)}, {int64(3)}, {int64(4)}}
	reversePage(page)
	if page[0][0] != int64(4) || page[3][0] != int64(1) {
		t.Errorf("reversed page edges: %v / %v", page[0][0], page[3][0])
	}
	reversePage(nil)
	reversePage(page[:1])
}
