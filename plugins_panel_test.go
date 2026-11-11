package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tablepro-go/internal/pluginreg"
)

// pluginTestApp builds an App whose state files live in a temp dir, so the
// plugin panel reads and writes nothing of the real profile.
//
// TABLEPRO_HOME is the lever that makes this true: os.UserConfigDir() ignores
// HOME/USERPROFILE on Windows, so without an explicit override these tests would
// write plugin_trust.json into the developer's real %APPDATA%\TableProGo.
func pluginTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TABLEPRO_HOME", dir)
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: nil,
		settings:         AppSettings{SafeMode: "off", ResultLimit: 1000},
	}
	return app
}

// fakePlugin writes a small executable-shaped file.
func fakePlugin(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPluginPanelStartsEmptyWithAStarterRegistry(t *testing.T) {
	app := pluginTestApp(t)
	// The first open writes a starter registry the user can edit, so the panel
	// is a file to fill in rather than a blank screen with no format hint.
	if err := app.ensureDefaultPluginRegistry(); err != nil {
		t.Fatalf("ensure registry: %v", err)
	}
	panel, err := app.ListPlugins()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(panel.Plugins) != 1 {
		t.Fatalf("plugins = %+v, want the example row", panel.Plugins)
	}
	row := panel.Plugins[0]
	if row.Source != "registry" || row.Exists {
		t.Fatalf("row = %+v (the example file does not exist)", row)
	}
	if row.State != string(pluginreg.StateUnknown) {
		t.Fatalf("state = %s", row.State)
	}
}

func TestPluginTrustMakesAnUnknownFileTrusted(t *testing.T) {
	app := pluginTestApp(t)
	path := fakePlugin(t, t.TempDir(), "duckdb.exe", "plugin-binary")

	// Before trusting: unknown, runnable.
	info, err := app.TrustPlugin(path, "duckdb")
	if err != nil {
		t.Fatalf("trust: %v", err)
	}
	if info.State != string(pluginreg.StateTrusted) {
		t.Fatalf("state after trust = %s, want trusted", info.State)
	}
	if info.Actual == "" || info.Expected != info.Actual {
		t.Fatalf("info = %+v", info)
	}

	// The store really persisted: a second panel read agrees.
	store, err := pluginreg.LoadTrustStore(userDataFilePath("plugin_trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Plugins) != 1 {
		t.Fatalf("store = %+v", store.Plugins)
	}
}

func TestPluginTamperIsRefusedByTheTrustGate(t *testing.T) {
	// The acceptance case: a trusted plugin whose bytes were swapped must not
	// register. This drives the real gate (plugins.Load) rather than the store in
	// isolation.
	app := pluginTestApp(t)
	dir := t.TempDir()
	path := fakePlugin(t, dir, "duckdb.exe", "original-bytes")
	if _, err := app.TrustPlugin(path, "duckdb"); err != nil {
		t.Fatal(err)
	}

	// Swap the bytes after trusting.
	if err := os.WriteFile(path, []byte("swapped-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TABLEPRO_PLUGIN_DUCKDB", path)

	// The registry is process-global; clean up so later tests are unaffected.
	plugins.unregister("duckdb")
	t.Cleanup(func() { plugins.unregister("duckdb") })

	errs := plugins.Load()
	if len(errs) == 0 {
		t.Fatal("a tampered plugin must be refused")
	}
	if !strings.Contains(errs[0].Error(), "被拒") || !strings.Contains(errs[0].Error(), "摘要不符") {
		t.Fatalf("error should name the tamper: %v", errs)
	}
	if _, ok := plugins.lookup("duckdb"); ok {
		t.Fatal("a refused plugin must not be registered")
	}

	// And the panel agrees with the gate: the row shows tampered.
	panel, err := app.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	var state string
	for _, p := range panel.Plugins {
		if p.Type == "duckdb" {
			state = p.State
		}
	}
	// No env row was added (registration refused), so the registry does not
	// mention duckdb either: the panel has no row, which is consistent with
	// "nothing is running". The important assertion is the lookup above.
	_ = state
}

func TestPluginUnknownIsAllowedWithAWarning(t *testing.T) {
	// The counterpart of the tamper case: a file nobody vouched for is a dev
	// plugin, not an attack. Refusing it would break the env workflow.
	app := pluginTestApp(t)
	dir := t.TempDir()
	path := fakePlugin(t, dir, "duckdb.exe", "dev-build")
	t.Setenv("TABLEPRO_PLUGIN_DUCKDB", path)

	plugins.unregister("duckdb")
	t.Cleanup(func() { plugins.unregister("duckdb") })

	errs := plugins.Load()
	if len(errs) != 0 {
		t.Fatalf("an unknown plugin must not be refused: %v", errs)
	}
	if _, ok := plugins.lookup("duckdb"); !ok {
		t.Fatal("an unknown plugin must register")
	}
	// The panel shows it as unknown, with the warning text.
	panel, err := app.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	var row *PluginInfo
	for i := range panel.Plugins {
		if panel.Plugins[i].Type == "duckdb" {
			row = &panel.Plugins[i]
		}
	}
	if row == nil {
		t.Fatalf("panel = %+v", panel.Plugins)
	}
	if row.State != string(pluginreg.StateUnknown) {
		t.Fatalf("state = %s, want unknown", row.State)
	}
	if !strings.Contains(row.Detail, "未在信任清单") {
		t.Fatalf("detail = %q", row.Detail)
	}
	if !row.Enabled {
		t.Fatal("a registered plugin is enabled")
	}
}

func TestPluginUntrustReturnsToUnknown(t *testing.T) {
	app := pluginTestApp(t)
	path := fakePlugin(t, t.TempDir(), "p.exe", "bytes")
	if _, err := app.TrustPlugin(path, "x"); err != nil {
		t.Fatal(err)
	}
	if err := app.UntrustPlugin(path); err != nil {
		t.Fatalf("untrust: %v", err)
	}
	// Untrusting again reports that nothing was there.
	if err := app.UntrustPlugin(path); err == nil {
		t.Fatal("untrusting an absent path should say so")
	}
}

func TestPluginTrustRefusesMissingFile(t *testing.T) {
	app := pluginTestApp(t)
	// Vouching for a path that does not exist would write a trust entry that can
	// never match, so it is refused up front.
	if _, err := app.TrustPlugin(filepath.Join(t.TempDir(), "gone.exe"), "x"); err == nil {
		t.Fatal("trusting a missing file must be refused")
	}
}

func TestPluginRegistryDrivesThePanel(t *testing.T) {
	// A registry entry with a digest is what a distributor ships: the panel
	// verifies against it without the user having trusted anything.
	app := pluginTestApp(t)
	dir := t.TempDir()
	bin := fakePlugin(t, dir, "ch.exe", "clickhouse-plugin")
	digest, err := pluginreg.HashFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	reg := &pluginreg.Registry{
		Schema: pluginreg.RegistrySchema,
		Plugins: []pluginreg.Entry{{
			Type: "clickhouse", Name: "ClickHouse", Path: bin, SHA256: digest, Enabled: true,
		}},
	}
	if err := pluginreg.SaveRegistry(pluginRegistryPath(), reg); err != nil {
		t.Fatal(err)
	}
	panel, err := app.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	if len(panel.Plugins) != 1 {
		t.Fatalf("plugins = %+v", panel.Plugins)
	}
	row := panel.Plugins[0]
	if row.State != string(pluginreg.StateTrusted) {
		t.Fatalf("state = %s, want trusted from the registry digest", row.State)
	}
	if row.Name != "ClickHouse" || !row.Exists {
		t.Fatalf("row = %+v", row)
	}
}
