package main

// PostgreSQL 的触发器读取：第 6 波的配方用了 MySQL 的列名
// （EVENT_OBJECT_MANIPULATION / STATUS），真库上一直报错、触发器卡片为空；
// 定义查询还把 pg_get_triggerdef 的完整 CREATE TRIGGER 又前缀了一次。这个用例
// 用真实 PostgreSQL 同时钉住列表与定义两条路径。

import (
	"strings"
	"testing"
)

// TestLiveTriggers lists a trigger and reads its definition on PostgreSQL.
// TABULA_LIVE_PG=postgres://postgres:dbcore@127.0.0.1:55432/dbcore enables it.
func TestLiveTriggers(t *testing.T) {
	app, database := livePostgresApp(t)
	db := app.connections["c1"].DB

	table := "tablepro_live_trg"
	fn := "tablepro_live_trg_fn"
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS ` + table,
		`DROP FUNCTION IF EXISTS ` + fn + `()`,
		`CREATE TABLE ` + table + ` (id int)`,
		`CREATE FUNCTION ` + fn + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		`CREATE TRIGGER ` + table + `_t AFTER INSERT ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION ` + fn + `()`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	defer db.Exec(`DROP TABLE IF EXISTS ` + table)
	defer db.Exec(`DROP FUNCTION IF EXISTS ` + fn + `()`)

	structure, err := app.GetTableStructure("c1", database, "public", table, "table")
	if err != nil {
		t.Fatalf("structure: %v", err)
	}
	if len(structure.Triggers) == 0 {
		t.Fatalf("no triggers read for %s (the PG catalog query is broken)", table)
	}
	tr := structure.Triggers[0]
	if tr.Name != table+"_t" || tr.Timing != "AFTER" || tr.Event != "INSERT" {
		t.Errorf("trigger = %+v, want %s_t AFTER INSERT", tr, table)
	}

	def, err := app.GetTriggerDefinition("c1", database, "public", table+"_t")
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(def)), "CREATE TRIGGER") {
		t.Errorf("definition is not CREATE TRIGGER DDL:\n%s", def)
	}
	if strings.Count(strings.ToUpper(def), "CREATE TRIGGER") != 1 {
		t.Errorf("definition repeats the CREATE TRIGGER prefix:\n%s", def)
	}
	if !strings.Contains(def, "EXECUTE FUNCTION") {
		t.Errorf("definition does not carry the trigger body:\n%s", def)
	}
}
