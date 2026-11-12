package main

// W9 end-to-end: the client's own path against a live Oracle.
//
// The kernel tests prove the recipes; this proves the wiring — connection lookup,
// Safe Mode, the "did the recompile actually fix it" re-read, and the panel's
// Supported flag for a non-Oracle connection.

import (
	"os"
	"strings"
	"testing"
)

// oracleApp connects a real Oracle and returns the App plus the connection id.
// Skipped unless TEST_ORACLE_DSN is set (same DSN shape the kernel tests use).
func oracleApp(t *testing.T) (*App, string) {
	t.Helper()
	dsn := os.Getenv("DBCORE_TEST_ORACLE_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_ORACLE_DSN to run the Oracle client tests")
	}
	app := pluginTestApp(t)
	cfg, err := ParseConnectionURI(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.Name = "ora-test"
	if err := app.AddConnection(cfg); err != nil {
		t.Fatalf("add connection: %v", err)
	}
	// AddConnection mints the id (a connection's identity is the app's, not the
	// caller's), so the id is read back rather than assumed.
	saved := app.GetConnections()
	if len(saved) == 0 {
		t.Fatal("the connection was not saved")
	}
	connID := saved[len(saved)-1].ID
	if err := app.Connect(connID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = app.Disconnect(connID) })
	return app, connID
}

func TestOracleInvalidObjectsPanel(t *testing.T) {
	app, connID := oracleApp(t)
	health := app.ListInvalidObjects(connID, "", "SYSTEM")
	if !health.Supported {
		t.Fatalf("Oracle must support the panel: %+v", health)
	}
	// The list is allowed to be empty (a clean schema); the point is that the call
	// succeeded rather than reporting an error.
	if health.Note != "" {
		t.Fatalf("unexpected note: %s", health.Note)
	}
}

func TestOraclePanelIsHiddenForNonOracle(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	health := app.ListInvalidObjects("c1", "", "")
	if health.Supported {
		t.Fatalf("a SQLite connection must not claim the Oracle panel: %+v", health)
	}
	if !strings.Contains(health.Note, "Oracle") {
		t.Fatalf("the note should say why: %q", health.Note)
	}
}

func TestOracleRecompileRequiresConfirmation(t *testing.T) {
	app, connID := oracleApp(t)
	// Without confirm, nothing is sent. The check is on the request, not just the
	// UI: a caller that forgets the flag must not recompile.
	res := app.RecompileObject(OracleRecompileRequest{
		ConnID: connID, Schema: "SYSTEM", Name: "SOMETHING", Type: "PROCEDURE",
	})
	if res.Error == "" || !strings.Contains(res.Error, "确认") {
		t.Fatalf("res = %+v", res)
	}
	if res.Compiled {
		t.Fatal("nothing may be compiled without confirmation")
	}
}

func TestOracleRecompileReportsPersistentInvalidity(t *testing.T) {
	// The live behaviour this whole feature had to be built around: Oracle accepts
	// ALTER … COMPILE even when the object cannot be compiled. The client must
	// therefore report StillInvalid and the errors, not just "compiled".
	app, connID := oracleApp(t)
	const proc = "TPG_W9_CLIENT_BROKEN"
	exec := func(sql string) error {
		if r := app.ExecuteQuery(connID, sql, ""); r.Error != "" {
			return &queryError{r.Error}
		}
		return nil
	}
	if err := exec("DROP PROCEDURE SYSTEM." + proc); err != nil {
		t.Logf("cleanup: %v", err)
	}
	t.Cleanup(func() {
		_ = exec("DROP PROCEDURE SYSTEM." + proc)
		_ = exec("DROP TABLE SYSTEM.TPG_W9_CLIENT_T")
	})
	for _, stmt := range []string{
		"CREATE TABLE SYSTEM.TPG_W9_CLIENT_T (id NUMBER)",
		"CREATE OR REPLACE PROCEDURE SYSTEM." + proc + " AS n NUMBER; " +
			"BEGIN SELECT COUNT(*) INTO n FROM SYSTEM.TPG_W9_CLIENT_T; END;",
		"DROP TABLE SYSTEM.TPG_W9_CLIENT_T",
	} {
		if err := exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// It is invalid now.
	health := app.ListInvalidObjects(connID, "", "SYSTEM")
	var found *OracleObject
	for i := range health.Objects {
		if strings.EqualFold(health.Objects[i].Name, proc) {
			found = &health.Objects[i]
		}
	}
	if found == nil {
		t.Fatalf("the broken procedure is not listed: %+v", health.Objects)
	}

	// Recompile it with the dependency still missing.
	res := app.RecompileObject(OracleRecompileRequest{
		ConnID: connID, Schema: "SYSTEM", Name: found.Name, Type: found.Type, Confirm: true,
	})
	if res.Error != "" {
		t.Fatalf("recompile request: %s", res.Error)
	}
	if !res.Compiled {
		t.Fatal("the statement itself should have run")
	}
	if !res.StillInvalid {
		t.Fatal("the object must be reported as still invalid: its table is gone")
	}
	if len(res.Errors) == 0 {
		t.Fatal("the compiler's errors must be reported")
	}
	var mentions bool
	for _, e := range res.Errors {
		if strings.Contains(strings.ToUpper(e.Text), "ORA-00942") ||
			strings.Contains(strings.ToUpper(e.Text), "TPG_W9_CLIENT_T") {
			mentions = true
		}
	}
	if !mentions {
		t.Fatalf("the errors should name the missing table: %+v", res.Errors)
	}
}

func TestOracleDependenciesPinTheTable(t *testing.T) {
	app, connID := oracleApp(t)
	const proc = "TPG_W9_CLIENT_DEP"
	const table = "TPG_W9_CLIENT_DEP_T"
	exec := func(sql string) error {
		if r := app.ExecuteQuery(connID, sql, ""); r.Error != "" {
			return &queryError{r.Error}
		}
		return nil
	}
	_ = exec("DROP PROCEDURE SYSTEM." + proc)
	t.Cleanup(func() {
		_ = exec("DROP PROCEDURE SYSTEM." + proc)
		_ = exec("DROP TABLE SYSTEM." + table)
	})
	for _, stmt := range []string{
		"CREATE TABLE SYSTEM." + table + " (id NUMBER)",
		"CREATE OR REPLACE PROCEDURE SYSTEM." + proc + " AS n NUMBER; " +
			"BEGIN SELECT COUNT(*) INTO n FROM SYSTEM." + table + "; END;",
	} {
		if err := exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	deps, err := app.ListObjectDependencies(connID, "", "SYSTEM", table)
	if err != nil {
		t.Fatalf("dependencies: %v", err)
	}
	var dependent bool
	for _, d := range deps {
		if d.Direction == "in" && strings.EqualFold(d.Name, proc) {
			dependent = true
		}
	}
	if !dependent {
		t.Fatalf("the procedure should depend on the table: %+v", deps)
	}
}

// queryError adapts the result struct's error string to an error.
type queryError struct{ msg string }

func (e *queryError) Error() string { return e.msg }
