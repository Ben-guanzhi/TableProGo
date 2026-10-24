package main

// Plugin resolution order: a registered plugin wins over the built-in engine.
//
// A plugin is a process, so it is slower to start and it costs a pipe per
// connection — but it is also the only way an engine this binary does not compile
// can be used, and it is how a user replaces a driver without a rebuild. Both
// reasons make "plugin first" the right default: someone who registered a plugin
// for postgres said they want that one, not the built-in one they already had.

import (
	"context"
	"fmt"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

// driverFor resolves the driver for a connection's engine.
//
// The order is: a registered plugin, then the built-in engine. The built-in path
// is what a machine with no plugins uses, so the binary keeps working as it
// always has.
func (a *App) driverForEngine(conn *Connection) (dbcore.SQLDriver, error) {
	if spec, ok := plugins.lookup(conn.Type); ok {
		drv, err := conn.startPlugin(spec)
		if err != nil {
			// A registered plugin that cannot start is reported, not silently
			// replaced: the user chose it, and quietly falling back would hide
			// the reason theirs does not run.
			return nil, fmt.Errorf("plugin for %s: %w", conn.Type, err)
		}
		return drv, nil
	}
	return a.builtinDriver(conn)
}

// builtinDriver is the engine compiled into this binary.
func (a *App) builtinDriver(conn *Connection) (dbcore.SQLDriver, error) {
	if conn.DB == nil {
		return nil, fmt.Errorf("not connected")
	}
	return driverFor(conn, conn.DB)
}

// hasBuiltinDriver reports whether the binary compiles a driver for an engine.
func hasBuiltinDriver(engineType string) bool {
	switch engineType {
	case "sqlite", "postgresql", "mysql", "sqlserver", "oracle":
		return true
	}
	return false
}

// pluginSpecFor reports the plugin registered for an engine, if any.
func pluginSpecFor(engineType string) (pluginSpec, bool) { return plugins.lookup(engineType) }

// RegisteredPlugins lists the engines served by a plugin, so the UI can show
// which connections are plugin-backed.
func RegisteredPlugins() []string {
	plugins.mu.RLock()
	defer plugins.mu.RUnlock()
	out := make([]string, 0, len(plugins.specs))
	for engineType := range plugins.specs {
		out = append(out, engineType)
	}
	return out
}

// startPluginConn launches a plugin in connection-oriented mode: one process
// serving several connections, the way a browser-style host does.
func (c *Connection) startPluginConn(spec pluginSpec) (*plugindriver.ConnDriver, error) {
	c.pluginMu.Lock()
	defer c.pluginMu.Unlock()
	if c.pluginConn != nil {
		return c.pluginConn, nil
	}
	host, err := plugindriver.StartConn(context.Background(), spec.Path, nil)
	if err != nil {
		return nil, err
	}
	if string(host.Kind()) != spec.Kind && spec.Kind != "" {
		_ = host.Close()
		return nil, fmt.Errorf("plugin %s speaks %q, but it is registered as %q", spec.Path, host.Kind(), spec.Kind)
	}
	c.pluginConn = host
	return host, nil
}
