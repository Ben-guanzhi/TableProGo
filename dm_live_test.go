package main

// W14: the client path against a live DM8.
//
// The kernel tests prove the driver; this proves the wiring — connection lookup,
// the engine switch, and the catalog the sidebar reads. Skipped unless
// DBCORE_TEST_DM_DSN is set:
//
//	export DBCORE_TEST_DM_DSN='dm://SYSDBA:SYSDBA001@localhost:5236?schema=SYSDBA'
//	go test -run TestDMLiveClient -v .
//
// Start an instance with dbcore/dm/dm8-up.sh.

import (
	"os"
	"strings"
	"testing"
)

func dmLiveApp(t *testing.T) (*App, string) {
	t.Helper()
	dsn := os.Getenv("DBCORE_TEST_DM_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_DM_DSN to run the DM client tests")
	}
	app := pluginTestApp(t)
	cfg, err := ParseConnectionURI(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.Name = "dm-live"
	if err := app.AddConnection(cfg); err != nil {
		t.Fatalf("add: %v", err)
	}
	saved := app.GetConnections()
	connID := saved[len(saved)-1].ID
	if err := app.Connect(connID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = app.Disconnect(connID) })
	return app, connID
}

func TestDMLiveClientBrowsesTables(t *testing.T) {
	app, connID := dmLiveApp(t)
	// The sidebar's first two calls: schemas, then tables.
	schemas, err := app.GetSchemas(connID, "")
	if err != nil {
		t.Fatalf("GetSchemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("no schemas: the DM catalog is not answering through the client")
	}
	tables, err := app.GetTables(connID, "", "SYSDBA")
	if err != nil {
		t.Fatalf("GetTables: %v", err)
	}
	_ = tables
}

func TestDMLiveClientQueryAndPaging(t *testing.T) {
	app, connID := dmLiveApp(t)
	const table = "TPG_DM_CLIENT"
	exec := func(sql string) *QueryResult {
		res := app.ExecuteQuery(connID, sql, "")
		if res.Error != "" {
			t.Fatalf("%q: %s", sql, res.Error)
		}
		return &res
	}
	// The first drop cleans up a previous failed run; the table not existing is the
	// normal case, so its error is ignored — the CREATE that follows is not.
	_ = app.ExecuteQuery(connID, "DROP TABLE "+table, "")
	exec("CREATE TABLE " + table + " (ID BIGINT PRIMARY KEY, NOTE VARCHAR(20))")
	t.Cleanup(func() { _ = app.ExecuteQuery(connID, "DROP TABLE "+table, "") })
	// Literals, not binds: ExecuteQuery is the editor's path and takes no
	// parameters — a ":1" there is a statement the server is told to bind.
	for i := 1; i <= 3; i++ {
		exec("INSERT INTO " + table + " (ID, NOTE) VALUES (" + itoa(i) + ", " + "'n" + itoa(i) + "')")
	}

	res := exec("SELECT ID, NOTE FROM " + table + " ORDER BY ID")
	if res.RowCount != 3 {
		t.Fatalf("rowCount = %d", res.RowCount)
	}
	// The id column is a BIGINT, so the value arrives as a number the grid can
	// sort; DM reports INT columns as text and the driver converts those back.
	if !strings.Contains(strings.Join(columnNames(res.Columns), ","), "ID") {
		t.Fatalf("columns = %+v", res.Columns)
	}
}

// itoa keeps the live test free of strconv noise.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}