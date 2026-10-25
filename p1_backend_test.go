package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The export cap is a setting: 0 keeps the built-in default, a configured
// value takes effect, and the refusal says so.
func TestExportMaxRowsSetting(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])

	// Default: two rows sail through.
	if _, err := app.InsertScript("c1", "", "", "parts", []string{"id", "name", "qty"},
		[][]interface{}{{int64(1), "a", int64(10)}, {int64(2), "b", int64(20)}}); err != nil {
		t.Fatalf("default export: %v", err)
	}

	// A tight configured cap refuses the same two rows and names the setting.
	app.settings = AppSettings{ResultLimit: 1000, ExportMaxRows: 1}
	_, err := app.InsertScript("c1", "", "", "parts", []string{"id", "name", "qty"},
		[][]interface{}{{int64(1), "a", int64(10)}, {int64(2), "b", int64(20)}})
	if err == nil || !strings.Contains(err.Error(), "导出行数上限") {
		t.Fatalf("configured cap should refuse with the setting named, got %v", err)
	}
}

// Grid edits land in the history as the statements the user reviewed.
func TestApplyChangesRecordsHistory(t *testing.T) {
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	seedParts(t, app.connections["c1"])

	set := partsSet(app.connections["c1"],
		RowChange{Original: []interface{}{1.0, "a", 10.0}, Edits: []CellEdit{{Column: "name", Value: "changed"}}})
	if _, err := app.ApplyChanges(set); err != nil {
		t.Fatalf("apply: %v", err)
	}

	history := app.History("", "", "", 10)
	found := false
	for _, h := range history {
		if strings.Contains(h.SQL, "changed") && strings.Contains(h.SQL, "UPDATE") {
			found = true
		}
	}
	if !found {
		t.Fatalf("grid edit missing from history: %+v", history)
	}
}

// A stored plain-text password (keyring was unavailable when it was saved) is
// something the user gets told about, not just the log.
func TestSecurityNoticeDetectsPlainPassword(t *testing.T) {
	dir := t.TempDir()
	app := &App{
		connections:      map[string]*Connection{},
		filePath:         filepath.Join(dir, "connections.json"),
		settingsPath:     filepath.Join(dir, "settings.json"),
		historyPath:      filepath.Join(dir, "history.json"),
		savedConnections: []ConnectionConfig{{ID: "c1", Name: "legacy", Type: "sqlite", Password: "hunter2"}},
	}
	if got := app.SecurityNotice(); got == "" || !strings.Contains(got, "明文") {
		t.Fatalf("notice = %q, want the plain-text warning", got)
	}
	// A sealed password raises nothing.
	app.savedConnections[0].Password = secretPrefix + "sealed"
	if got := app.SecurityNotice(); got != "" {
		t.Fatalf("sealed password should be silent, got %q", got)
	}
}
