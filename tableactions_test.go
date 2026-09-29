package main

import (
	"strings"
	"testing"
)

func actionStatement(t *testing.T, o objectNames, action string) string {
	t.Helper()
	statement, err := tableActionStatement(o, action)
	if err != nil {
		t.Fatalf("%s failed: %v", action, err)
	}
	return statement
}

func TestTableActionStatementsByDialect(t *testing.T) {
	cases := []struct {
		name     string
		object   objectNames
		action   string
		expected string
	}{
		{
			name:     "drop a sqlite table",
			object:   objectNames{dbType: "sqlite", objectType: "table", table: "parts"},
			action:   actionDrop,
			expected: `DROP TABLE "parts"`,
		},
		{
			name:     "drop a mysql view",
			object:   objectNames{dbType: "mysql", objectType: "view", database: "shop", table: "cheap"},
			action:   actionDrop,
			expected: "DROP VIEW `shop`.`cheap`",
		},
		{
			name:     "truncate keeps the schema",
			object:   objectNames{dbType: "sqlserver", objectType: "table", database: "erp", schema: "sales", table: "orders"},
			action:   actionTruncate,
			expected: "TRUNCATE TABLE [erp].[sales].[orders]",
		},
		{
			// SQLite has no TRUNCATE; the CLI's own way is a DELETE with no
			// condition, which leaves the autoincrement counter alone.
			name:     "sqlite clears with a delete",
			object:   objectNames{dbType: "sqlite", objectType: "table", table: "parts"},
			action:   actionTruncate,
			expected: `DELETE FROM "parts"`,
		},
		{
			name:     "mysql renames both sides",
			object:   objectNames{dbType: "mysql", objectType: "table", database: "shop", table: "parts", newName: "pieces"},
			action:   actionRename,
			expected: "RENAME TABLE `shop`.`parts` TO `shop`.`pieces`",
		},
		{
			name:     "sql server renames through its procedure",
			object:   objectNames{dbType: "sqlserver", objectType: "table", database: "erp", schema: "sales", table: "orders", newName: "history"},
			action:   actionRename,
			expected: `EXEC sp_rename N'sales.orders', N'history'`,
		},
		{
			name:     "postgres renames within its schema",
			object:   objectNames{dbType: "postgresql", objectType: "table", schema: "sales", table: "orders", newName: "history"},
			action:   actionRename,
			expected: `ALTER TABLE "sales"."orders" RENAME TO "history"`,
		},
		{
			name:     "a quoted name survives the quoting",
			object:   objectNames{dbType: "postgresql", objectType: "table", schema: "public", table: `we"ird`, newName: "fine"},
			action:   actionDrop,
			expected: `DROP TABLE "public"."we""ird"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := actionStatement(t, tc.object, tc.action); got != tc.expected {
				t.Errorf("want %s\ngot  %s", tc.expected, got)
			}
		})
	}
}

func TestTableActionsThatAreRefused(t *testing.T) {
	cases := []struct {
		name   string
		object objectNames
		action string
		want   string
	}{
		{
			name:   "a view has nothing to clear",
			object: objectNames{dbType: "mysql", objectType: "view", database: "shop", table: "cheap"},
			action: actionTruncate,
			want:   "视图没有数据可清空",
		},
		{
			// The new name is an identifier, so anything that could move the
			// object into another schema has to go.
			name:   "a new name carrying a qualifier",
			object: objectNames{dbType: "postgresql", objectType: "table", schema: "public", table: "a", newName: "public.b"},
			action: actionRename,
			want:   "not a valid new name",
		},
		{
			name:   "a new name carrying a quote",
			object: objectNames{dbType: "sqlite", objectType: "table", table: "a", newName: `b"; DROP TABLE c`},
			action: actionRename,
			want:   "not a valid new name",
		},
		{
			name:   "renaming to the name it has",
			object: objectNames{dbType: "sqlite", objectType: "table", table: "a", newName: "a"},
			action: actionRename,
			want:   "新名字和原来一样",
		},
		{
			name:   "an action the menu never offers",
			object: objectNames{dbType: "sqlite", objectType: "table", table: "a"},
			action: "VACUUM",
			want:   "unknown table action",
		},
		{
			name:   "a database this build cannot drive",
			object: objectNames{dbType: "duckdb", objectType: "table", table: "a"},
			action: actionTruncate,
			want:   "unsupported type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tableActionStatement(tc.object, tc.action)
			if err == nil {
				t.Fatalf("want an error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// The menu's job ends at naming the object; the proof is that the statement
// actually moves the table on a real database.
func TestTableActionRunsAgainstSqlite(t *testing.T) {
	conn := sqliteDB(t, "conn_act", "scratch")
	seedParts(t, conn)
	a := searchApp(conn)

	if _, err := a.TableAction(conn.ID, conn.Database, "", "parts", actionRename, "pieces"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := tableState(t, conn, "SELECT id, name FROM pieces ORDER BY id"); strings.Join(got, " / ") != "1|a / 2|b" {
		t.Errorf("renamed table is missing its rows: %v", got)
	}
	if _, err := a.TableAction(conn.ID, conn.Database, "", "pieces", actionTruncate, ""); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if got := tableState(t, conn, "SELECT COUNT(*) FROM pieces"); got[0] != "0" {
		t.Errorf("truncate left rows behind: %v", got)
	}
	if _, err := a.TableAction(conn.ID, conn.Database, "", "pieces", actionDrop, ""); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := a.TableAction(conn.ID, conn.Database, "", "pieces", actionTruncate, ""); err == nil {
		t.Errorf("the dropped table is still there")
	}
}

// The name has to be one the catalog actually has: that is what makes an odd
// name safe to quote, and a stale tree honest about failing rather than
// silently dropping whatever the request named.
func TestTableActionNeedsTheObjectToBeThere(t *testing.T) {
	conn := sqliteDB(t, "conn_act_verify", "scratch")
	seedParts(t, conn)
	a := searchApp(conn)

	if _, err := a.TableAction(conn.ID, conn.Database, "", "gone", actionDrop, ""); err == nil {
		t.Fatalf("want an error for an object that is not in the catalog")
	} else if !strings.Contains(err.Error(), "不在这个库里") {
		t.Errorf("unexpected error: %v", err)
	}
	// A rename that would sit on top of an existing table is refused before
	// anything runs, so no database has to be asked to drop one of them.
	if _, err := conn.DB.Exec(`CREATE TABLE other (id integer primary key)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := a.TableAction(conn.ID, conn.Database, "", "parts", actionRename, "other"); err == nil {
		t.Fatalf("want an error for a name that is taken")
	} else if !strings.Contains(err.Error(), "已经存在") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := conn.DB.Exec(`SELECT * FROM parts`); err != nil {
		t.Errorf("the refused rename changed the table anyway: %v", err)
	}
}

// The collision check has always compared names lowercased, so a rename that
// differs only in case still lands on an existing table - the probe asked of
// the catalog must find it there.
func TestTableActionRenameCollisionIgnoresCase(t *testing.T) {
	conn := sqliteDB(t, "conn_act_case", "scratch")
	seedParts(t, conn)
	if _, err := conn.DB.Exec(`CREATE TABLE "OTHER" (id integer primary key)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	a := searchApp(conn)
	if _, err := a.TableAction(conn.ID, conn.Database, "", "parts", actionRename, "other"); err == nil {
		t.Fatalf("want an error for a name taken in another case")
	} else if !strings.Contains(err.Error(), "已经存在") {
		t.Errorf("unexpected error: %v", err)
	}
}

// An action is recorded like any statement, so the history page can show what
// the menu did on the user's behalf.
func TestTableActionIsRecordedInHistory(t *testing.T) {
	conn := sqliteDB(t, "conn_act_log", "scratch")
	seedParts(t, conn)
	a := searchApp(conn)

	statement, err := a.TableAction(conn.ID, conn.Database, "", "parts", actionTruncate, "")
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if !strings.Contains(statement, `DELETE FROM "parts"`) {
		t.Errorf("unexpected statement: %s", statement)
	}
	entries := a.History(conn.ID, conn.Database, "parts", 10)
	if len(entries) == 0 {
		t.Fatalf("nothing recorded in the history")
	}
	if entries[0].SQL != statement {
		t.Errorf("history holds %q, want %q", entries[0].SQL, statement)
	}
}
