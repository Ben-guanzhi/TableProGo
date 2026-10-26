package main

import (
	"context"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dbcore/dbcore"
)

func redisTestApp(t *testing.T) (*App, *miniredis.Miniredis, string) {
	t.Helper()
	mr := miniredis.RunT(t)
	host, portText, err := net.SplitHostPort(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	config := ConnectionConfig{
		ID: "redis-1", Name: "cache", Type: "redis",
		Host: host, Port: port, Database: "0",
	}
	a := &App{
		connections:      map[string]*Connection{},
		savedConnections: []ConnectionConfig{config},
	}
	if err := a.Connect(config.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { a.Disconnect(config.ID) })
	return a, mr, config.ID
}

func TestRedisBrowseDatabasesAndKeys(t *testing.T) {
	a, mr, id := redisTestApp(t)
	mr.Set("greeting", "hello")
	mr.HSet("user:1", "name", "Ada")
	mr.HSet("user:1", "role", "admin")
	mr.RPush("queue", "first", "second")
	mr.SAdd("tags", "x", "y")
	mr.ZAdd("rank", 1.5, "alice")

	databases, err := a.GetDatabases(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(databases) != redisDatabases || databases[0] != "0" || databases[len(databases)-1] != "15" {
		t.Errorf("databases = %v, want 0..15", databases)
	}

	schemas, err := a.GetSchemas(id, "0")
	if err != nil || len(schemas) != 0 {
		t.Errorf("schemas = %v, err = %v; want no schemas", schemas, err)
	}

	tables, err := a.GetTables(id, "0", "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, table := range tables {
		names = append(names, table.Name)
	}
	want := []string{"greeting", "queue", "rank", "tags", "user:1"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("keys = %v, want %v", names, want)
	}

	if columns, err := a.GetColumns(id, "0", "", "greeting"); err != nil || len(columns) != 0 {
		t.Errorf("columns = %v, err = %v; want none", columns, err)
	}
}

func TestRedisKeyValueShapes(t *testing.T) {
	a, mr, id := redisTestApp(t)
	mr.Set("greeting", "hello")
	mr.HSet("user:1", "name", "Ada")
	mr.HSet("user:1", "role", "admin")
	mr.RPush("queue", "first", "second")
	mr.SAdd("tags", "x", "y")
	mr.ZAdd("rank", 1.5, "alice")
	mr.SetTTL("greeting", 30*time.Second)

	cases := []struct {
		key     string
		kind    string
		columns []string
		rows    [][]interface{}
	}{
		{"greeting", "string", []string{"value"}, [][]interface{}{{"hello"}}},
		{"queue", "list", []string{"index", "value"}, [][]interface{}{{int64(0), "first"}, {int64(1), "second"}}},
		{"tags", "set", []string{"member"}, [][]interface{}{{"x"}, {"y"}}},
		{"rank", "zset", []string{"member", "score"}, [][]interface{}{{"alice", 1.5}}},
		{"user:1", "hash", []string{"field", "value"}, [][]interface{}{{"name", "Ada"}, {"role", "admin"}}},
	}
	for _, c := range cases {
		value, err := a.GetKVValue(id, "0", c.key)
		if err != nil {
			t.Errorf("%s: %v", c.key, err)
			continue
		}
		if value.Key != c.key || value.Type != c.kind {
			t.Errorf("%s: key/type = %q/%q, want %q/%q", c.key, value.Key, value.Type, c.key, c.kind)
		}
		var columns []string
		for _, col := range value.Result.Columns {
			columns = append(columns, col.Name)
		}
		if !reflect.DeepEqual(columns, c.columns) {
			t.Errorf("%s: columns = %v, want %v", c.key, columns, c.columns)
		}
		if !reflect.DeepEqual(value.Result.Rows, c.rows) {
			t.Errorf("%s: rows = %#v, want %#v", c.key, value.Result.Rows, c.rows)
		}
	}

	value, err := a.GetKVValue(id, "0", "greeting")
	if err != nil {
		t.Fatal(err)
	}
	if value.TTL == "永不过期" || !strings.HasSuffix(value.TTL, "s") {
		t.Errorf("TTL = %q, want a remaining lifetime", value.TTL)
	}

	if _, err := a.GetKVValue(id, "0", "missing"); err == nil {
		t.Error("missing key read without an error")
	}
}

func TestRedisCommandExecutionAndDatabaseIsolation(t *testing.T) {
	a, mr, id := redisTestApp(t)
	mr.Set("greeting", "hello")

	result := a.ExecuteQuery(id, "GET greeting", "0")
	if result.Error != "" || len(result.Rows) != 1 || result.Rows[0][0] != "hello" {
		t.Fatalf("GET = %#v", result)
	}

	result = a.ExecuteQuery(id, `SET "space key" "hello world"`, "0")
	if result.Error != "" {
		t.Fatalf("SET: %s", result.Error)
	}
	if got, _ := mr.Get("space key"); got != "hello world" {
		t.Errorf("space key = %q", got)
	}

	if result := a.ExecuteQuery(id, "SET other value", "1"); result.Error != "" {
		t.Fatalf("SET db1: %s", result.Error)
	}
	if keys, _ := a.GetTables(id, "0", ""); containsKey(keys, "other") {
		t.Error("db 1 key listed under db 0")
	}
	if keys, _ := a.GetTables(id, "1", ""); !containsKey(keys, "other") {
		t.Error("db 1 key missing from db 1")
	}
	value, err := a.GetKVValue(id, "1", "other")
	if err != nil || value.Result.Rows[0][0] != "value" {
		t.Errorf("db1 value = %#v, err = %v", value, err)
	}

	// SELECT moves the shared client; the next request must put it back on the
	// database it asked for.
	if result := a.ExecuteQuery(id, "SELECT 2", ""); result.Error != "" {
		t.Fatalf("SELECT: %s", result.Error)
	}
	value, err = a.GetKVValue(id, "", "greeting")
	if err != nil || value.Result.Rows[0][0] != "hello" {
		t.Errorf("default database read after SELECT = %#v, err = %v", value, err)
	}
}

func containsKey(tables []TableInfo, key string) bool {
	for _, table := range tables {
		if table.Name == key {
			return true
		}
	}
	return false
}

func TestSplitRedisCommand(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
		bad  bool
	}{
		{"GET key", []string{"GET", "key"}, false},
		{"  SET   k   v  ", []string{"SET", "k", "v"}, false},
		{`SET k "hello world"`, []string{"SET", "k", "hello world"}, false},
		{`SET k 'a "b" c'`, []string{"SET", "k", `a "b" c`}, false},
		{`SET k a\ b`, []string{"SET", "k", "a b"}, false},
		{`SET k ""`, []string{"SET", "k", ""}, false},
		{`GET "unterminated`, nil, true},
		{`GET k\`, nil, true},
		{"", nil, false},
	} {
		got, err := splitRedisCommand(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("splitRedisCommand(%q) = %#v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitRedisCommand(%q): %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitRedisCommand(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestRedisKeyAdmin(t *testing.T) {
	a, mr, id := redisTestApp(t)
	mr.Set("greeting", "hello")
	mr.Set("other", "keep")
	mr.SetTTL("greeting", 30*time.Second)

	info, err := a.GetKVKeyInfo(id, "0", "greeting")
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != "string" || info.TTL == "永不过期" {
		t.Errorf("key info = %#v, want a string with a TTL", info)
	}

	if err := a.SetKVKeyTTL(id, "0", "greeting", 60); err != nil {
		t.Fatal(err)
	}
	if ttl := mr.TTL("greeting"); ttl <= 30*time.Second || ttl > 60*time.Second {
		t.Errorf("TTL after SetKVKeyTTL = %v, want about a minute", ttl)
	}
	if err := a.SetKVKeyTTL(id, "0", "greeting", 0); err != nil {
		t.Fatal(err)
	}
	if ttl := mr.TTL("greeting"); ttl != 0 {
		t.Errorf("TTL after clearing = %v, want 0", ttl)
	}
	if err := a.SetKVKeyTTL(id, "0", "missing", 10); err == nil {
		t.Error("TTL on a missing key succeeded")
	}

	// A rename keeps the value and its remaining lifetime.
	mr.SetTTL("greeting", time.Minute)
	if err := a.RenameKVKey(id, "0", "greeting", "welcome"); err != nil {
		t.Fatal(err)
	}
	if got, _ := mr.Get("welcome"); got != "hello" {
		t.Errorf("renamed value = %q, want hello", got)
	}
	if ttl := mr.TTL("welcome"); ttl <= 0 {
		t.Errorf("renamed key TTL = %v, want preserved", ttl)
	}
	if _, err := a.GetKVValue(id, "0", "greeting"); err == nil {
		t.Error("old key survived rename")
	}
	if err := a.RenameKVKey(id, "0", "welcome", "other"); err == nil {
		t.Error("rename overwrote an existing key")
	}
	if got, _ := mr.Get("other"); got != "keep" {
		t.Errorf("collision target changed to %q", got)
	}
	if err := a.RenameKVKey(id, "0", "welcome", "welcome"); err != nil {
		t.Errorf("renaming a key to itself: %v", err)
	}

	if err := a.DeleteKVKey(id, "0", "welcome"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetKVKeyInfo(id, "0", "welcome"); err == nil {
		t.Error("deleted key still reports info")
	}
	// DEL is idempotent, and so is this wrapper.
	if err := a.DeleteKVKey(id, "0", "welcome"); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
}

func TestRedisSetElement(t *testing.T) {
	a, mr, id := redisTestApp(t)
	mr.Set("greeting", "hello")
	mr.HSet("user:1", "name", "Ada")
	mr.HSet("user:1", "role", "admin")
	mr.RPush("queue", "first", "second")
	mr.ZAdd("rank", 1.5, "alice")
	mr.SetTTL("user:1", 5*time.Minute)

	for _, tc := range []struct {
		name string
		elem dbcore.RedisElement
	}{
		{"string", dbcore.RedisElement{Key: "greeting", Identity: "key", Column: "value", Value: "hi"}},
		{"hash", dbcore.RedisElement{Key: "user:1", Identity: "field", ID: "name", Column: "value", Value: "Grace"}},
		{"list", dbcore.RedisElement{Key: "queue", Identity: "index", ID: "1", Column: "value", Value: "updated"}},
		{"zset", dbcore.RedisElement{Key: "rank", Identity: "member", ID: "alice", Column: "score", Value: "2.5"}},
	} {
		if err := a.SetKVElement(id, "0", tc.elem); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}

	if got, _ := mr.Get("greeting"); got != "hi" {
		t.Errorf("string = %q, want hi", got)
	}
	if got := mr.HGet("user:1", "name"); got != "Grace" {
		t.Errorf("hash field = %q, want Grace", got)
	}
	if ttl := mr.TTL("user:1"); ttl <= 0 {
		t.Errorf("hash TTL = %v, want preserved", ttl)
	}
	value, err := a.GetKVValue(id, "0", "queue")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value.Result.Rows[1], []interface{}{int64(1), "updated"}) {
		t.Errorf("list row = %#v", value.Result.Rows[1])
	}
	value, err = a.GetKVValue(id, "0", "rank")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value.Result.Rows[0], []interface{}{"alice", 2.5}) {
		t.Errorf("zset row = %#v", value.Result.Rows[0])
	}

	if err := a.SetKVElement(id, "0", dbcore.RedisElement{Key: "user:1", Identity: "member", ID: "x", Column: "score", Value: "1"}); err == nil {
		t.Error("hash field written as a sorted-set member")
	}
}

func TestRedisAddRow(t *testing.T) {
	a, mr, id := redisTestApp(t)
	mr.SAdd("tags", "go")
	mr.SetTTL("tags", 5*time.Minute)
	mr.XAdd("events", "1-1", []string{"kind", "seed"})
	mr.SetTTL("events", 5*time.Minute)

	member, err := a.AddKVRow(id, "0", dbcore.RedisRow{Key: "tags", Kind: dbcore.RedisSet, Member: "redis"})
	if err != nil || member != "redis" {
		t.Fatalf("add set member = %q, %v", member, err)
	}
	if ok, err := mr.SIsMember("tags", "redis"); err != nil || !ok {
		t.Errorf("redis member missing: %v, %v", ok, err)
	}
	if ttl := mr.TTL("tags"); ttl <= 0 {
		t.Errorf("set TTL = %v, want preserved", ttl)
	}
	// Adding the same member again is a no-op, not an error.
	if member, err := a.AddKVRow(id, "0", dbcore.RedisRow{Key: "tags", Kind: dbcore.RedisSet, Member: "redis"}); err != nil || member != "redis" {
		t.Errorf("duplicate member = %q, %v", member, err)
	}
	if members, err := mr.Members("tags"); err != nil || len(members) != 2 {
		t.Errorf("members = %v, %v", members, err)
	}

	assigned, err := a.AddKVRow(id, "0", dbcore.RedisRow{
		Key: "events", Kind: dbcore.RedisStream,
		Fields: []dbcore.RedisField{{Name: "kind", Value: "new"}, {Name: "n", Value: "1"}},
	})
	if err != nil || assigned == "" || assigned == "1-1" {
		t.Fatalf("add stream entry = %q, %v", assigned, err)
	}
	value, err := a.GetKVValue(id, "0", "events")
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Result.Rows) != 2 {
		t.Fatalf("stream rows = %d, want 2", len(value.Result.Rows))
	}
	entry, _ := value.Result.Rows[1][0].(string)
	if !strings.HasPrefix(entry, assigned+" ") || !strings.Contains(entry, "kind=new") || !strings.Contains(entry, "n=1") {
		t.Errorf("stream entry = %q, want id %q with both fields", entry, assigned)
	}
	if ttl := mr.TTL("events"); ttl <= 0 {
		t.Errorf("stream TTL = %v, want preserved", ttl)
	}

	bad := []dbcore.RedisRow{
		{Kind: dbcore.RedisSet, Member: "x"},
		{Key: "tags", Kind: dbcore.RedisHash, Member: "x"},
		{Key: "tags", Kind: dbcore.RedisSet},
		{Key: "events", Kind: dbcore.RedisStream},
	}
	for _, row := range bad {
		if _, err := a.AddKVRow(id, "0", row); err == nil {
			t.Errorf("AddKVRow(%+v) should fail", row)
		}
	}
}

type pagedKV struct {
	dbcore.KVDriver
	pages map[uint64]dbcore.RedisScanPage
	calls int
}

func (p *pagedKV) ScanKeys(ctx context.Context, cursor uint64, pattern string, count int) (*dbcore.RedisScanPage, error) {
	p.calls++
	page := p.pages[cursor]
	page.Keys = append([]string(nil), page.Keys...)
	return &page, nil
}

func TestRedisKeyPagination(t *testing.T) {
	a, _, id := redisTestApp(t)
	conn := a.connections[id]
	conn.KV = &pagedKV{
		KVDriver: conn.KV,
		pages: map[uint64]dbcore.RedisScanPage{
			0: {Cursor: 7, Keys: []string{"b", "a"}},
			7: {Cursor: 0, Keys: []string{"c", "a"}},
		},
	}

	page, err := a.GetKVKeyPage(id, "0", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Cursor != "7" || page.Done || !reflect.DeepEqual(page.Keys, []string{"a", "b"}) {
		t.Errorf("first page = %#v", page)
	}
	next, err := a.GetKVKeyPage(id, "0", page.Cursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next.Cursor != "0" || !next.Done || !reflect.DeepEqual(next.Keys, []string{"a", "c"}) {
		t.Errorf("second page = %#v", next)
	}
	if conn.KV.(*pagedKV).calls != 2 {
		t.Errorf("ScanKeys calls = %d, want 2", conn.KV.(*pagedKV).calls)
	}
	if _, err := a.GetKVKeyPage(id, "0", "not-a-cursor", 0); err == nil {
		t.Error("invalid cursor accepted")
	}
}

// A collection larger than the window must come back in windows, and a list's
// index column must carry the item's real rank across a continuation — the grid
// edits list items by index (LSET), so renumbering a window would point an
// edit at the wrong item. Small keys stay a single done window.
func TestRedisValueWindowing(t *testing.T) {
	a, mr, id := redisTestApp(t)
	const total = kvValueRowLimit + 25
	for i := 0; i < total; i++ {
		mr.RPush("biglist", "item-"+strconv.Itoa(i))
	}

	first, err := a.GetKVValue(id, "0", "biglist")
	if err != nil {
		t.Fatal(err)
	}
	if first.Result.RowCount != kvValueRowLimit || !first.Result.Truncated || first.Done {
		t.Fatalf("first window = %d rows truncated=%v done=%v", first.Result.RowCount, first.Result.Truncated, first.Done)
	}
	if first.Next == "" {
		t.Fatal("no continuation cursor")
	}
	if got, _ := first.Result.Rows[0][0].(int64); got != 0 {
		t.Errorf("first row index = %v, want 0", got)
	}

	second, err := a.GetKVValuePage(id, "0", "biglist", first.Next)
	if err != nil {
		t.Fatal(err)
	}
	if second.Result.RowCount != 25 || !second.Done || second.Next != "" {
		t.Fatalf("second window = %d rows, done=%v next=%q", second.Result.RowCount, second.Done, second.Next)
	}
	// Index 1000 is the rank the item really has, not 0 inside the window.
	wantFirst := int64(kvValueRowLimit)
	if got, _ := second.Result.Rows[0][0].(int64); got != wantFirst {
		t.Fatalf("continuation index = %v, want %v", got, wantFirst)
	}
	if got, _ := second.Result.Rows[0][1].(string); got != "item-1000" {
		t.Errorf("continuation value = %q, want item-1000", got)
	}

	// Editing a continuation row addresses the right item (the rank is the id).
	if err := a.SetKVElement(id, "0", dbcore.RedisElement{
		Key: "biglist", Identity: "index", ID: strconv.FormatInt(wantFirst, 10), Column: "value", Value: "edited",
	}); err != nil {
		t.Fatalf("edit continuation row: %v", err)
	}
	items, err := mr.List("biglist")
	if err != nil || len(items) <= int(wantFirst) || items[wantFirst] != "edited" {
		t.Errorf("list item %d not edited (len=%d, err=%v)", wantFirst, len(items), err)
	}

	// A string never enters the paged path.
	mr.Set("greeting", "hello")
	small, err := a.GetKVValue(id, "0", "greeting")
	if err != nil || small.Result.RowCount != 1 || !small.Done || small.Next != "" {
		t.Fatalf("string read = %#v, %v", small, err)
	}
}

// A hash window loop must visit each field exactly once: HSCAN counts are
// hints, so the fill loop de-duplicates; the sorted rendering matches the old
// whole-key read.
func TestRedisHashWindowStability(t *testing.T) {
	a, mr, id := redisTestApp(t)
	const total = kvValueRowLimit + 10
	for i := 0; i < total; i++ {
		mr.HSet("universe", "f"+strconv.Itoa(i), "v"+strconv.Itoa(i))
	}
	seen := map[string]bool{}
	cursor := ""
	windows := 0
	for {
		page, err := a.GetKVValuePage(id, "0", "universe", cursor)
		if err != nil {
			t.Fatal(err)
		}
		windows++
		for _, row := range page.Result.Rows {
			field, _ := row[0].(string)
			if seen[field] {
				t.Fatalf("field %q repeated across windows", field)
			}
			seen[field] = true
		}
		if page.Done {
			break
		}
		cursor = page.Next
		if windows > 10 {
			t.Fatal("no progress made in 10 windows")
		}
	}
	if len(seen) != total {
		t.Errorf("collected %d fields, want %d", len(seen), total)
	}
}

func TestRedisDisconnectClosesDriver(t *testing.T) {
	a, mr, id := redisTestApp(t)
	mr.Set("greeting", "hello")
	if err := a.Disconnect(id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetKVValue(id, "0", "greeting"); err == nil {
		t.Error("read after disconnect succeeded")
	}
	if _, err := a.GetDatabases(id); err == nil {
		t.Error("database list after disconnect succeeded")
	}
}
