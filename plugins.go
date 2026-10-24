package main

// Plugin drivers: an engine this binary cannot compile can still be browsed and
// queried, by running it in a child process that speaks the dbcore plugin protocol
// (github.com/dbcore/dbcore/plugindriver).
//
// The interesting part is not the launch — it is that a plugin-backed connection has
// no *sql.DB, so every path that assumed one has to be answered deliberately:
//
//   - metadata and Execute go through the plugin's driver (driverForPlugin);
//   - a browse goes through BrowseRows, which the plugin renders itself;
//   - row editing needs SQL text built for a table the plugin owns, so it uses the
//     plugin's own Execute rather than the client's update builder;
//   - a transaction cannot be composed here, because the kernel's *sql.Tx has no
//     meaning for a child process: the tab reports that instead of pretending.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

// pluginSpec describes how to launch one engine's plugin.
type pluginSpec struct {
	// Path is the executable. A bare name is looked up on PATH.
	Path string
	// Kind is the engine the plugin claims; the banner must agree.
	Kind string
}

// pluginRegistry maps an engine type onto its plugin. Populated from the environment
// so a user can add an engine without a rebuild:
//
//	TABLEPRO_PLUGIN_<TYPE>=/path/to/plugin[;arg;arg]
//
// The type is upper-cased, so a Postgres plugin is TABLEPRO_PLUGIN_POSTGRESQL.
type pluginRegistry struct {
	mu    sync.RWMutex
	specs map[string]pluginSpec
}

var plugins = &pluginRegistry{specs: map[string]pluginSpec{}}

// LoadPlugins reads the environment for plugin declarations. Called once at startup.
func (r *pluginRegistry) Load() []error {
	var errs []error
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "TABLEPRO_PLUGIN_") || value == "" {
			continue
		}
		typ := strings.ToLower(strings.TrimPrefix(name, "TABLEPRO_PLUGIN_"))
		// A relative path is resolved now: the child's working directory is the
		// process's, which is not necessarily where the user typed the command.
		path := value
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if _, err := os.Stat(path); err != nil {
			errs = append(errs, fmt.Errorf("plugin for %s: %w", typ, err))
			continue
		}
		r.specs[typ] = pluginSpec{Path: path, Kind: typ}
	}
	return errs
}

// lookup reports the plugin registered for an engine type.
func (r *pluginRegistry) lookup(typ string) (pluginSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.specs[typ]
	return spec, ok
}

// register records one plugin (used by tests and by Load).
func (r *pluginRegistry) register(typ, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs[typ] = pluginSpec{Path: path, Kind: typ}
}

// startPlugin launches the plugin for a connection and keeps it on the Connection.
// The process lives as long as the connection, so browsing does not pay a launch per
// keystroke.
func (c *Connection) startPlugin(spec pluginSpec) (dbcore.SQLDriver, error) {
	c.pluginMu.Lock()
	defer c.pluginMu.Unlock()
	if c.plugin != nil {
		return c.plugin, nil
	}
	drv, err := plugindriver.Start(context.Background(), spec.Path, nil)
	if err != nil {
		return nil, err
	}
	if string(drv.Kind()) != spec.Kind && spec.Kind != "" {
		// The banner is authoritative: a plugin that claims a different engine would
		// have this client render its dialect wrong.
		_ = drv.Close()
		return nil, fmt.Errorf("plugin %s speaks %q, but it is registered as %q", spec.Path, drv.Kind(), spec.Kind)
	}
	c.plugin = drv
	return drv, nil
}

// pluginDriver returns the connection's plugin driver, starting it on first use.
func (a *App) pluginDriver(conn *Connection) (dbcore.SQLDriver, error) {
	spec, ok := plugins.lookup(conn.Type)
	if !ok {
		return nil, fmt.Errorf("%w: no plugin is registered for %q", dbcore.ErrNotSupported, conn.Type)
	}
	return conn.startPlugin(spec)
}

// closePlugin shuts the child process down; called from Disconnect. Both shapes
// are closed: the statement-oriented driver and the connection-oriented host.
func (c *Connection) closePlugin() {
	c.pluginMu.Lock()
	drv := c.plugin
	host := c.pluginConn
	c.plugin = nil
	c.pluginConn = nil
	c.pluginMu.Unlock()
	if drv != nil {
		_ = drv.Close()
	}
	if host != nil {
		_ = host.Close()
	}
}

// pluginKind reports whether a connection is served by a plugin.
func pluginConnection(conn *Connection) bool {
	if conn.DB != nil {
		return false
	}
	_, ok := plugins.lookup(conn.Type)
	return ok
}
