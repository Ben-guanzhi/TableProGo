package main

import "testing"

// movesDataOnly 的判据：只有纯 DML 才不算"可能改目录"。
func TestMovesDataOnly(t *testing.T) {
	dataOnly := []string{
		"INSERT INTO t VALUES (1)",
		"insert into t values (1)",
		"  \n\t UPDATE t SET a = 1",
		"DELETE FROM t WHERE a = 1",
		"MERGE INTO t USING s ON 1=1",
		"REPLACE INTO t VALUES (1)",
	}
	for _, q := range dataOnly {
		if !movesDataOnly(q) {
			t.Errorf("movesDataOnly(%q) = false, want true", q)
		}
	}
	catalogChanging := []string{
		"CREATE TABLE t(v integer)",
		"ALTER TABLE t ADD a integer",
		"DROP TABLE t",
		"TRUNCATE TABLE t",
		"RENAME TABLE a TO b",
		"SELECT 1",
		"PRAGMA foo",
		"WITH x AS (SELECT 1) SELECT * FROM x",
		"VACUUM",
	}
	for _, q := range catalogChanging {
		if movesDataOnly(q) {
			t.Errorf("movesDataOnly(%q) = true, want false", q)
		}
	}
}

// catalogVersion 只在可能改目录的语句上 +1：纯 DML 不再让 quick-open 快照
// 在每次编辑后重建。
func TestCatalogVersionOnlyBumpsForCatalogChangingStatements(t *testing.T) {
	conn := sqliteDB(t, "conn_0", "scratch")
	a := searchApp(conn)

	base := a.catalogVersion.Load()
	if r := a.runQuery("conn_0", "CREATE TABLE t(v integer)", ""); r.Error != "" {
		t.Fatalf("create: %v", r.Error)
	}
	if got := a.catalogVersion.Load(); got != base+1 {
		t.Fatalf("CREATE bumped version to %d, want %d", got, base+1)
	}

	base = a.catalogVersion.Load()
	for _, q := range []string{
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET v = 2",
		"DELETE FROM t",
	} {
		if r := a.runQuery("conn_0", q, ""); r.Error != "" {
			t.Fatalf("%q: %v", q, r.Error)
		}
	}
	if got := a.catalogVersion.Load(); got != base {
		t.Fatalf("DML bumped version to %d, want unchanged %d", got, base)
	}

	if r := a.runQuery("conn_0", "DROP TABLE t", ""); r.Error != "" {
		t.Fatalf("drop: %v", r.Error)
	}
	if got := a.catalogVersion.Load(); got != base+1 {
		t.Fatalf("DROP bumped version to %d, want %d", got, base+1)
	}
}

// 过滤器缓存以连接 UUID 为键，删连接不会移位——缓存只需删除对应键，
// 其它连接的缓存条目不受影响。
func TestSavedDatabaseFilterInvalidatedOnDelete(t *testing.T) {
	a := &App{
		connections: map[string]*Connection{},
		savedConnections: []ConnectionConfig{
			{ID: "conn_0", DatabaseFilter: DatabaseFilter{Exclude: []string{"zero"}}},
			{ID: "conn_1", DatabaseFilter: DatabaseFilter{Exclude: []string{"one"}}},
		},
	}

	// Prime the cache for conn_1.
	if f := a.savedDatabaseFilter("conn_1"); len(f.Exclude) != 1 || f.Exclude[0] != "one" {
		t.Fatalf("primed wrong filter: %+v", f)
	}

	if err := a.DeleteConnection("conn_0"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// conn_1 keeps its ID; its cache entry must still be valid.
	f := a.savedDatabaseFilter("conn_1")
	if len(f.Exclude) != 1 || f.Exclude[0] != "one" {
		t.Fatalf("stale filter after delete: %+v (want exclude=one)", f)
	}
}

// 更新连接的过滤器同样要让缓存失效。
func TestSavedDatabaseFilterInvalidatedOnUpdate(t *testing.T) {
	a := &App{
		connections: map[string]*Connection{},
		savedConnections: []ConnectionConfig{
			{ID: "conn_0", DatabaseFilter: DatabaseFilter{Exclude: []string{"old"}}},
			{ID: "conn_1", DatabaseFilter: DatabaseFilter{Exclude: []string{"one"}}},
		},
	}
	if f := a.savedDatabaseFilter("conn_0"); len(f.Exclude) != 1 || f.Exclude[0] != "old" {
		t.Fatalf("primed wrong filter: %+v", f)
	}
	// Prime conn_1's cache too; updating conn_0 must not affect it.
	if f := a.savedDatabaseFilter("conn_1"); len(f.Exclude) != 1 || f.Exclude[0] != "one" {
		t.Fatalf("primed wrong filter for conn_1: %+v", f)
	}
	if err := a.UpdateConnection(ConnectionConfig{
		ID:             "conn_0",
		DatabaseFilter: DatabaseFilter{Exclude: []string{"new"}},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	f := a.savedDatabaseFilter("conn_0")
	if len(f.Exclude) != 1 || f.Exclude[0] != "new" {
		t.Fatalf("stale filter after update: %+v (want exclude=new)", f)
	}
	// conn_1's cache must still be valid.
	f1 := a.savedDatabaseFilter("conn_1")
	if len(f1.Exclude) != 1 || f1.Exclude[0] != "one" {
		t.Fatalf("conn_1 filter should be unchanged: %+v (want exclude=one)", f1)
	}
}
