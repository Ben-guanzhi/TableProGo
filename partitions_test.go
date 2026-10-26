package main

// 分区与枚举类型的消费端：分区进结构页卡片（表级），枚举类型进 schema 级浏览
// 对话框（PostgreSQL 专属）。SQLite 两者都没有：卡片不出现、对话框给明确说明。

import (
	"strings"
	"testing"
)

// A plain SQLite table reports no partitions (the concept is unsupported, so the
// card must stay hidden rather than show an empty section).
func TestPartitionsAbsentOnSQLite(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])

	structure, err := app.GetTableStructure("c1", "", "", "parts", "table")
	if err != nil {
		t.Fatalf("structure: %v", err)
	}
	if len(structure.Partitions) != 0 {
		t.Errorf("partitions = %+v, want none (SQLite has no partitioning)", structure.Partitions)
	}
}

func TestEnumTypesUnsupportedOnSQLite(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if _, err := app.GetEnumTypes("c1", "", ""); err == nil || !strings.Contains(err.Error(), "没有用户定义枚举类型") {
		t.Fatalf("GetEnumTypes error = %v, want the unsupported message", err)
	}
}

// TestLivePartitionsAndEnums checks the PostgreSQL path end to end: a partitioned
// table's partitions reach the structure page, and a CREATE TYPE reaches the enum
// list with its labels in declaration order.
// TABULA_LIVE_PG=postgres://postgres:dbcore@127.0.0.1:55432/dbcore enables it.
func TestLivePartitionsAndEnums(t *testing.T) {
	app, database := livePostgresApp(t)
	db := app.connections["c1"].DB

	table := "tablepro_live_part"
	enum := "tablepro_live_mood"
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS ` + table + ` CASCADE`,
		`DROP TYPE IF EXISTS ` + enum,
		`CREATE TABLE ` + table + ` (id int, qty int) PARTITION BY RANGE (qty)`,
		`CREATE TABLE ` + table + `_p0 PARTITION OF ` + table + ` FOR VALUES FROM (0) TO (10)`,
		`CREATE TABLE ` + table + `_p1 PARTITION OF ` + table + ` FOR VALUES FROM (10) TO (100)`,
		`CREATE TYPE ` + enum + ` AS ENUM ('sad', 'ok', 'happy')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	defer db.Exec(`DROP TABLE IF EXISTS ` + table + ` CASCADE`)
	defer db.Exec(`DROP TYPE IF EXISTS ` + enum)

	structure, err := app.GetTableStructure("c1", database, "public", table, "table")
	if err != nil {
		t.Fatalf("structure: %v", err)
	}
	if len(structure.Partitions) != 2 {
		t.Fatalf("partitions = %+v, want 2", structure.Partitions)
	}
	for i, want := range []string{table + "_p0", table + "_p1"} {
		if structure.Partitions[i].Name != want {
			t.Errorf("partition %d name = %q, want %q", i, structure.Partitions[i].Name, want)
		}
		if !strings.Contains(structure.Partitions[i].Bound, "FOR VALUES") {
			t.Errorf("partition %d bound = %q, want the FOR VALUES text", i, structure.Partitions[i].Bound)
		}
	}

	// A plain table's structure must not carry partitions.
	plain, err := app.GetTableStructure("c1", database, "public", "parts", "table")
	if err != nil {
		t.Fatalf("plain structure: %v", err)
	}
	if len(plain.Partitions) != 0 {
		t.Errorf("plain table reports partitions: %+v", plain.Partitions)
	}

	enums, err := app.GetEnumTypes("c1", database, "public")
	if err != nil {
		t.Fatalf("GetEnumTypes: %v", err)
	}
	for _, e := range enums {
		if e.Name != enum {
			continue
		}
		if strings.Join(e.Labels, ",") != "sad,ok,happy" {
			t.Errorf("enum labels = %v, want declaration order sad,ok,happy", e.Labels)
		}
		return
	}
	t.Fatalf("enum type %q missing from %+v", enum, enums)
}
