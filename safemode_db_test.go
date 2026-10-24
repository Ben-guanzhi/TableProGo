package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestAppWithSQLite builds the minimal App wiring a query needs: one saved
// connection, an open connection map and the given Safe Mode.
func newTestAppWithSQLite(t *testing.T, mode string) (*App, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "safe.db")
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: []ConnectionConfig{{ID: "c1", Name: "safe", Type: "sqlite", Database: path}},
		settings:         AppSettings{SafeMode: mode, ResultLimit: 1000},
	}
	if err := app.Connect("c1"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = app.Disconnect("c1") })

	for _, q := range []string{
		"CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)",
		"INSERT INTO t(id,name) VALUES (1,'a'),(2,'b')",
	} {
		if r := app.ExecuteQuery("c1", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}
	return app, path
}

func rowCount(t *testing.T, app *App) int {
	t.Helper()
	r := app.ExecuteQuery("c1", "SELECT id FROM t", "")
	if r.Error != "" {
		t.Fatalf("count: %s", r.Error)
	}
	return r.RowCount
}

func TestSafeModeBlockNoWhereRefusesDelete(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeBlockNoWhere)
	if got := rowCount(t, app); got != 2 {
		t.Fatalf("rows before = %d", got)
	}

	r := app.ExecuteQuery("c1", "DELETE FROM t", "")
	if r.Error == "" || !strings.Contains(r.Error, "Safe Mode") {
		t.Fatalf("expected a Safe Mode block, got %+v", r)
	}
	if r.NeedsConfirmation {
		t.Fatalf("blocked statements must not ask for confirmation: %+v", r)
	}
	if got := rowCount(t, app); got != 2 {
		t.Fatalf("blocked DELETE still ran: rows = %d", got)
	}

	// "blockNoWhere" is a hard block: confirming must NOT bypass it (that is
	// what the confirm levels are for). The statement still never reaches the DB.
	r = app.ExecuteQueryConfirmed("c1", "DELETE FROM t", "")
	if r.Error == "" || !strings.Contains(r.Error, "Safe Mode") {
		t.Fatalf("confirmed delete must still be blocked in block mode, got %+v", r)
	}
	if got := rowCount(t, app); got != 2 {
		t.Fatalf("rows after confirmed delete = %d, want 2", got)
	}
}

func TestSafeModeConfirmNoWherePausesAndRunsWhenConfirmed(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeConfirmNoWhere)

	r := app.ExecuteQuery("c1", "UPDATE t SET name = 'x'", "")
	if !r.NeedsConfirmation {
		t.Fatalf("expected NeedsConfirmation, got %+v", r)
	}
	if got := rowCount(t, app); got != 2 {
		t.Fatalf("paused UPDATE ran anyway: rows = %d", got)
	}

	r = app.ExecuteQueryConfirmed("c1", "UPDATE t SET name = 'x'", "")
	if r.Error != "" {
		t.Fatalf("confirmed update: %s", r.Error)
	}
	if got := rowCount(t, app); got != 2 {
		t.Fatalf("update should keep both rows, got %d", got)
	}
}

func TestSafeModeAllowsGuardedStatements(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeBlockNoWhere)

	// WHERE present → passes without confirmation.
	if r := app.ExecuteQuery("c1", "DELETE FROM t WHERE id = 1", ""); r.Error != "" {
		t.Fatalf("guarded delete should pass: %s", r.Error)
	}
	if got := rowCount(t, app); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}

	// INSERT/DDL carry no row filter and stay allowed in the WHERE modes.
	if r := app.ExecuteQuery("c1", "INSERT INTO t(id,name) VALUES (3,'c')", ""); r.Error != "" {
		t.Fatalf("insert should pass: %s", r.Error)
	}
}

func TestSafeModeSettingsPersistNormalized(t *testing.T) {
	app := &App{settingsPath: filepath.Join(t.TempDir(), "settings.json")}
	if err := app.SaveSettings(AppSettings{SafeMode: "blockNoWhere", ResultLimit: 500}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := app.GetSettings().SafeMode; got != safeModeBlockNoWhere {
		t.Fatalf("stored mode = %q", got)
	}
	// Unknown values must fall back to "off" instead of silently blocking.
	if err := app.SaveSettings(AppSettings{SafeMode: "whatever"}); err != nil {
		t.Fatalf("save unknown: %v", err)
	}
	if got := app.GetSettings().SafeMode; got != safeModeOff {
		t.Fatalf("unknown mode should normalize to off, got %q", got)
	}
	_ = time.Now
}
