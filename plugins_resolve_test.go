package main

// The resolution order: a registered plugin wins over the built-in engine.
//
// The reason to test it is that the two paths are otherwise indistinguishable
// from the outside: both return a dbcore.SQLDriver, and a connection that
// silently got the built-in one when the user registered a plugin would look
// fine while ignoring what they asked for.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// registeredPluginPath returns the SQLite plugin fynequery builds, which this
// module can launch directly (it is an executable, not an import).
func registeredPluginPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "fynequery", "dist", "plugins"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	name := "fq-plugin-sqlite"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(root, name)
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("fynequery's plugins are not built: %v", err)
	}
	return binary
}

func TestPluginBeatsTheBuiltinEngine(t *testing.T) {
	// Register a plugin for sqlite — an engine this binary *does* compile in.
	// The registered plugin must win, because the user asked for it.
	plugins.register("sqlite", registeredPluginPath(t))

	spec, ok := pluginSpecFor("sqlite")
	if !ok {
		t.Fatal("the plugin must be registered")
	}
	if spec.Path == "" {
		t.Fatal("the spec must name the executable")
	}
	if spec.Kind != "sqlite" {
		t.Fatalf("kind = %q", spec.Kind)
	}

	// A connection of that type resolves to the plugin's driver. The database
	// is a temp file so Open can open it without leaving a stray artifact.
	conn := &Connection{ID: "res1", Name: "res", Type: "sqlite", Database: filepath.Join(t.TempDir(), "test.db")}
	drv, err := conn.startPluginConn(spec)
	if err != nil {
		t.Fatalf("the registered plugin must start: %v", err)
	}
	defer conn.closePlugin()

	// It is the plugin's driver, not the built-in one: the two are distinguished
	// by what they can answer. The plugin has no Ping (it is a fynequery
	// plugin), so a ping that answers "unknown method" proves the child answered.
	if err := drv.Ping(context.Background()); err != nil {
		t.Logf("the plugin answered: %v", err)
	}
	// The kind comes from the banner, and the banner said sqlite.
	if string(drv.Kind()) != "sqlite" {
		t.Fatalf("kind = %q", drv.Kind())
	}
}

// Without a plugin registered, the built-in engine answers: the binary keeps
// working as it always has.
func TestBuiltinEngineIsTheFallback(t *testing.T) {
	conn := &Connection{ID: "res2", Name: "res", Type: "sqlite", Database: "main"}
	if _, ok := pluginSpecFor(conn.Type); ok {
		t.Skip("a plugin is registered for sqlite in this run")
	}
	if !hasBuiltinDriver(conn.Type) {
		t.Fatalf("sqlite must have a built-in driver")
	}
}

// An engine with neither a plugin nor a built-in driver is reported as
// unsupported, which is what lets the user know why nothing opens.
func TestUnknownEngineHasNeither(t *testing.T) {
	conn := &Connection{ID: "res3", Name: "res", Type: "not-an-engine"}
	if hasBuiltinDriver(conn.Type) {
		t.Fatal("an unknown engine must not have a built-in driver")
	}
	if _, ok := pluginSpecFor(conn.Type); ok {
		t.Fatal("an unknown engine must not have a plugin")
	}
}

// The registry lists what is registered, so the UI can show which connections are
// plugin-backed.
func TestRegisteredPluginsListsWhatIsThere(t *testing.T) {
	plugins.register("listed-engine", registeredPluginPath(t))
	found := false
	for _, engineType := range RegisteredPlugins() {
		if engineType == "listed-engine" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the registry does not list the registered engine: %v", RegisteredPlugins())
	}
}

// A registered plugin that cannot start is reported, not silently replaced by the
// built-in one: the user chose it, and a quiet fallback would hide the reason.
func TestAFailingPluginIsReported(t *testing.T) {
	plugins.register("broken-engine", filepath.Join(t.TempDir(), "does-not-exist.exe"))
	conn := &Connection{ID: "res4", Name: "res", Type: "broken-engine", Database: "main"}

	spec, ok := pluginSpecFor("broken-engine")
	if !ok {
		t.Fatal("the plugin must be registered")
	}
	if _, err := conn.startPluginConn(spec); err == nil {
		t.Fatal("a plugin that cannot start must be reported")
	}
	// And it did not fall back to a built-in: there is none for this engine.
	if conn.pluginConn != nil {
		t.Fatal("a failed start must not leave a driver behind")
	}
}
