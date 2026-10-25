package main

import (
	"os"
	"testing"

	"github.com/dbcore/dbcore/sqltext"
)

func TestImportUpsertUpdatesExistingRows(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE up(id INTEGER PRIMARY KEY, name TEXT)", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	if r := app.ExecuteQuery("c1", "INSERT INTO up VALUES (1,'old'),(2,'two')", ""); r.Error != "" {
		t.Fatalf("seed: %s", r.Error)
	}
	path := writeTempFile(t, "up.csv", "id,name\n1,new\n3,three\n")

	res, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "up", Path: path,
		Options: ImportOptions{HasHeader: true, Mode: "upsert"},
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Rows != 2 {
		t.Fatalf("result = %+v", res)
	}
	r := app.ExecuteQuery("c1", "SELECT id, name FROM up ORDER BY id", "")
	if r.Error != "" || r.RowCount != 3 {
		t.Fatalf("rows = %+v", r)
	}
	if r.Rows[0][1] != "new" {
		t.Fatalf("existing row not updated: %+v", r.Rows)
	}
	if r.Rows[2][1] != "three" {
		t.Fatalf("new row missing: %+v", r.Rows)
	}
}

func TestImportUpsertRejectsUnknownKey(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	if r := app.ExecuteQuery("c1", "CREATE TABLE up2(id INTEGER PRIMARY KEY, name TEXT)", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	path := writeTempFile(t, "up2.csv", "id,name\n1,a\n")
	_, err := app.ImportFile(ImportRequest{
		ConnID: "c1", Table: "up2", Path: path,
		Options: ImportOptions{HasHeader: true, Mode: "upsert", UpsertKeys: []string{"nope"}},
	})
	if err == nil {
		t.Fatal("expected an error for an unknown conflict key")
	}
}

func TestImportUpsertRejectedOnMSSQLAndOracle(t *testing.T) {
	for _, typ := range []string{"sqlserver", "oracle"} {
		if _, err := sqltext.UpsertClause(typ, []string{"id", "name"}, []string{"id"}); err == nil {
			t.Errorf("%s: upsert must be rejected, not silently ignored", typ)
		}
	}
}

// The upsert clause must be the engine's own syntax.
func TestImportUpsertClausePerEngine(t *testing.T) {
	my, err := sqltext.UpsertClause("mysql", []string{"id", "name"}, []string{"id"})
	if err != nil || my != " ON DUPLICATE KEY UPDATE `id`=VALUES(`id`), `name`=VALUES(`name`)" {
		t.Fatalf("mysql clause = %q (%v)", my, err)
	}
	pg, err := sqltext.UpsertClause("postgresql", []string{"id", "name"}, []string{"id"})
	if err != nil || pg != ` ON CONFLICT ("id") DO UPDATE SET "id"=excluded."id", "name"=excluded."name"` {
		t.Fatalf("pg clause = %q (%v)", pg, err)
	}
	sq, err := sqltext.UpsertClause("sqlite", []string{"id"}, []string{"id"})
	if err != nil || sq != ` ON CONFLICT ("id") DO UPDATE SET "id"=excluded."id"` {
		t.Fatalf("sqlite clause = %q (%v)", sq, err)
	}
}

// Live engines: MySQL uses ON DUPLICATE KEY, PostgreSQL the ON CONFLICT form.
func TestLiveImportUpsert(t *testing.T) {
	cases := []struct{ env, typ, name string }{
		{"TABULA_LIVE_PG", "postgresql", "dbcore_upsert"},
		{"TABULA_LIVE_MYSQL", "mysql", "dbcore_upsert"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.typ, func(t *testing.T) {
			if os.Getenv(c.env) == "" {
				t.Skipf("%s not set", c.env)
			}
			app := liveImportApp(t, c.env, c.typ)
			_ = app.ExecuteQuery("c1", "DROP TABLE IF EXISTS "+c.name, "")
			t.Cleanup(func() { _ = app.ExecuteQuery("c1", "DROP TABLE IF EXISTS "+c.name, "") })

			// The table must carry the constraint the upsert targets: inference
			// deliberately creates plain columns, so declare it by hand here.
			create := map[string]string{
				"postgresql": `CREATE TABLE "` + c.name + `" (id BIGINT PRIMARY KEY, name TEXT)`,
				"mysql":      "CREATE TABLE `" + c.name + "` (id BIGINT PRIMARY KEY, name TEXT)",
			}[c.typ]
			if r := app.ExecuteQuery("c1", create, ""); r.Error != "" {
				t.Fatalf("create: %s", r.Error)
			}

			seed := writeTempFile(t, "seed.csv", "id,name\n1,old\n2,two\n")
			if _, err := app.ImportFile(ImportRequest{
				ConnID: "c1", Table: c.name, Path: seed,
				Options: ImportOptions{HasHeader: true},
			}); err != nil {
				t.Fatalf("seed import: %v", err)
			}

			update := writeTempFile(t, "upd.csv", "id,name\n1,new\n3,three\n")
			if _, err := app.ImportFile(ImportRequest{
				ConnID: "c1", Table: c.name, Path: update,
				Options: ImportOptions{HasHeader: true, Mode: "upsert", UpsertKeys: []string{"id"}},
			}); err != nil {
				t.Fatalf("upsert import: %v", err)
			}

			r := app.ExecuteQuery("c1", "SELECT id, name FROM "+c.name+" ORDER BY id", "")
			if r.Error != "" {
				t.Fatalf("select: %s", r.Error)
			}
			if r.RowCount != 3 {
				t.Fatalf("rows = %d, want 3 (1 updated, 2 kept, 3 added)", r.RowCount)
			}
			if r.Rows[0][1] != "new" || r.Rows[1][1] != "two" || r.Rows[2][1] != "three" {
				t.Fatalf("rows = %+v", r.Rows)
			}
		})
	}
}
