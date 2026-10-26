package main

// P2-6 的消费端：视图定义、CHECK 约束卡片与「函数与过程」对话框各自读
// dbcore.ExtraMetadataReader。SQLite 没有 CHECK/例程目录，测试同时钉住"没有概念
// 时不出卡片、不报错"的行为；真实例程走 TABULA_LIVE_PG 门禁的端到端用例。

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// structure of a view carries its definition; a table never does.
func TestViewStructureCarriesDefinition(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])
	if _, err := app.connections["c1"].DB.Exec(`CREATE VIEW parts_view AS SELECT id, name FROM parts`); err != nil {
		t.Fatalf("create view: %v", err)
	}

	view, err := app.GetTableStructure("c1", "", "", "parts_view", "view")
	if err != nil {
		t.Fatalf("view structure: %v", err)
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(view.ViewDefinition)), "CREATE") {
		t.Fatalf("view definition is not DDL: %q", view.ViewDefinition)
	}
	if !strings.Contains(view.ViewDefinition, "parts") {
		t.Errorf("view definition does not reference the base table: %q", view.ViewDefinition)
	}

	table, err := app.GetTableStructure("c1", "", "", "parts", "table")
	if err != nil {
		t.Fatalf("table structure: %v", err)
	}
	if table.ViewDefinition != "" {
		t.Errorf("a table's structure must not carry a view definition: %q", table.ViewDefinition)
	}
}

// SQLite has no CHECK-constraint catalog: the page still loads and the card
// stays empty instead of surfacing a driver error.
func TestCheckConstraintsAbsentOnSQLite(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if _, err := app.connections["c1"].DB.Exec(`CREATE TABLE checked(id INTEGER, qty INTEGER CHECK (qty > 0))`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	structure, err := app.GetTableStructure("c1", "", "", "checked", "table")
	if err != nil {
		t.Fatalf("structure: %v", err)
	}
	if len(structure.Checks) != 0 {
		t.Errorf("checks = %+v, want none (SQLite has no check catalog)", structure.Checks)
	}
}

// SQLite has no routines: both calls answer a clear message, not an empty list.
func TestRoutinesUnsupportedOnSQLite(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if _, err := app.GetRoutines("c1", "", ""); err == nil || !strings.Contains(err.Error(), "没有函数/过程目录") {
		t.Fatalf("GetRoutines error = %v, want the unsupported message", err)
	}
	if _, err := app.GetRoutineDefinition("c1", "", "", "f"); err == nil || !strings.Contains(err.Error(), "没有函数/过程目录") {
		t.Fatalf("GetRoutineDefinition error = %v, want the unsupported message", err)
	}
}

// TestLiveRoutines exercises the same path against a real PostgreSQL: list the
// schema's routines, read one definition, and drop the fixture again.
// TABULA_LIVE_PG=postgres://postgres:dbcore@127.0.0.1:55432/dbcore enables it.
func TestLiveRoutines(t *testing.T) {
	raw := os.Getenv("TABULA_LIVE_PG")
	if raw == "" {
		t.Skip("TABULA_LIVE_PG not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	port := 0
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			t.Fatalf("bad port: %v", err)
		}
	}
	cfg := ConnectionConfig{
		ID:       "c1",
		Name:     "live",
		Type:     "postgresql",
		Host:     u.Hostname(),
		Port:     port,
		Username: u.User.Username(),
		Database: strings.TrimPrefix(u.Path, "/"),
		SSLMode:  "false",
	}
	cfg.Password, _ = u.User.Password()
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: []ConnectionConfig{cfg},
		settings:         AppSettings{SafeMode: safeModeOff, ResultLimit: 1000},
	}
	if err := app.Connect("c1"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = app.Disconnect("c1") }()

	name := "tablepro_live_fn_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := app.connections["c1"].DB.Exec(
		`CREATE FUNCTION ` + name + `() RETURNS int LANGUAGE sql AS 'SELECT 1'`); err != nil {
		t.Fatalf("create function: %v", err)
	}
	defer app.connections["c1"].DB.Exec(`DROP FUNCTION ` + name + `()`)

	routines, err := app.GetRoutines("c1", cfg.Database, "public")
	if err != nil {
		t.Fatalf("GetRoutines: %v", err)
	}
	found := false
	for _, r := range routines {
		if r.Name == name {
			found = true
			if r.Kind != "FUNCTION" {
				t.Errorf("kind = %q, want FUNCTION", r.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("routine %q missing from %+v", name, routines)
	}

	def, err := app.GetRoutineDefinition("c1", cfg.Database, "public", name)
	if err != nil {
		t.Fatalf("GetRoutineDefinition: %v", err)
	}
	if !strings.Contains(def, name) || !strings.Contains(strings.ToUpper(def), "CREATE") {
		t.Errorf("definition does not describe the routine:\n%s", def)
	}
}
