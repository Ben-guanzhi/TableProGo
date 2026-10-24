package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// A .sql dump must run through the real engines' transaction and quoting paths,
// not just SQLite's. The script creates its own table, so this also proves the
// import does not depend on the dialog's table field.
func TestLiveImportSQLScript(t *testing.T) {
	cases := []struct{ env, typ string }{
		{"TABULA_LIVE_PG", "postgresql"},
		{"TABULA_LIVE_MYSQL", "mysql"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.typ, func(t *testing.T) {
			if os.Getenv(c.env) == "" {
				t.Skipf("%s not set", c.env)
			}
			app := liveImportApp(t, c.env, c.typ)
			table := fmt.Sprintf("dbcore_dump_%d", time.Now().UnixNano()%1_000_000)

			create := map[string]string{
				"postgresql": `CREATE TABLE "` + table + `" (id BIGINT PRIMARY KEY, name TEXT);`,
				"mysql":      "CREATE TABLE `" + table + "` (id BIGINT PRIMARY KEY, name TEXT);",
			}[c.typ]
			ins := map[string]string{
				"postgresql": `INSERT INTO "` + table + `" VALUES (1,'a; still one value'),(2,'b');`,
				"mysql":      "INSERT INTO `" + table + "` VALUES (1,'a; still one value'),(2,'b');",
			}[c.typ]
			path := writeTempFile(t, "dump.sql", create+"\n"+ins+"\n")

			res, err := app.ImportSQLScript(ImportRequest{ConnID: "c1", Path: path})
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if res.Statements != 2 {
				t.Fatalf("statements = %d", res.Statements)
			}
			t.Cleanup(func() {
				drop := map[string]string{
					"postgresql": `DROP TABLE IF EXISTS "` + table + `"`,
					"mysql":      "DROP TABLE IF EXISTS `" + table + "`",
				}[c.typ]
				_ = app.ExecuteQuery("c1", drop, "")
			})

			ref := table
			if c.typ == "postgresql" {
				ref = `"` + table + `"`
			} else {
				ref = "`" + table + "`"
			}
			r := app.ExecuteQuery("c1", "SELECT id, name FROM "+ref+" ORDER BY id", "")
			if r.Error != "" {
				t.Fatalf("select: %s", r.Error)
			}
			if r.RowCount != 2 || r.Rows[0][1] != "a; still one value" {
				t.Fatalf("rows = %+v", r.Rows)
			}
		})
	}
}
