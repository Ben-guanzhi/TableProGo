package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// liveImportApp builds an App with one saved connection from a TABULA_LIVE_*
// URL (the same variables the other live tests use).
func liveImportApp(t *testing.T, env, typ string) *App {
	t.Helper()
	raw := os.Getenv(env)
	if raw == "" {
		t.Skipf("%s not set", env)
	}
	cfg := connectionConfigFromURL(t, typ, raw)
	cfg.ID = "c1"
	cfg.Name = "live"
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: []ConnectionConfig{cfg},
		settings:         AppSettings{ResultLimit: 1000},
	}
	if err := app.Connect("c1"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = app.Disconnect("c1") })
	return app
}

// Import must work through the real per-engine placeholder, DDL and batch code
// paths, not just SQLite's.
func TestLiveImportCSV(t *testing.T) {
	cases := []struct{ env, typ string }{
		{"TABULA_LIVE_PG", "postgresql"},
		{"TABULA_LIVE_MYSQL", "mysql"},
		{"TABULA_LIVE_ORACLE", "oracle"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.typ, func(t *testing.T) {
			app := liveImportApp(t, c.env, c.typ)
			table := fmt.Sprintf("dbcore_imp_%d", time.Now().UnixNano()%1_000_000)
			path := writeTempFile(t, "rows.csv", "id,name,score\n1,alice,1.5\n2,bob,2\n3,carol,3.25\n")
			res, err := app.ImportFile(ImportRequest{
				ConnID: "c1", Table: table, Path: path,
				Options: ImportOptions{HasHeader: true, CreateTable: true, InferTypes: true},
			})
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if !res.CreatedTable || res.Rows != 3 || res.Skipped != 0 {
				t.Fatalf("result = %+v", res)
			}
			t.Cleanup(func() {
				drop := ""
				switch c.typ {
				case "mysql":
					drop = "DROP TABLE `" + table + "`"
				case "postgresql", "oracle":
					drop = `DROP TABLE "` + table + `"`
				}
				if drop != "" {
					_ = app.ExecuteQuery("c1", drop, "")
				}
			})

			// The three rows must be readable back through the normal query path.
			// Oracle is case-sensitive for quoted identifiers, so the table is
			// referenced quoted there as well.
			r := app.ExecuteQuery("c1", "SELECT id, name FROM "+table+" ORDER BY id", "")
			if r.Error != "" {
				t.Fatalf("select back: %s", r.Error)
			}
			if r.RowCount != 3 {
				t.Fatalf("rows = %d, want 3", r.RowCount)
			}
		})
	}
}
