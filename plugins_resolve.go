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
	"strconv"

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
		drv, err := conn.startPluginConn(spec)
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

// startPluginConn launches a plugin in connection-oriented mode (one process
// serving several connections, the way a browser-style host does) and opens the
// connection this Connection represents on it. The bound driver is returned; it
// is what every catalog and query call goes through.
func (c *Connection) startPluginConn(spec pluginSpec) (dbcore.SQLDriver, error) {
	c.pluginMu.Lock()
	defer c.pluginMu.Unlock()
	if c.pluginConn != nil && c.pluginConnID != "" {
		return c.pluginConn.Bind(c.pluginConnID, c.Database), nil
	}
	host, err := plugindriver.StartConn(context.Background(), spec.Path, nil)
	if err != nil {
		return nil, err
	}
	if string(host.Kind()) != spec.Kind && spec.Kind != "" {
		_ = host.Close()
		return nil, fmt.Errorf("plugin %s speaks %q, but it is registered as %q", spec.Path, host.Kind(), spec.Kind)
	}
	connID, _, err := host.Open(context.Background(), plugindriver.ConnectParams{
		Name:     c.Name,
		Host:     c.Host,
		Port:     strconv.Itoa(c.Port),
		User:     c.Username,
		Password: c.Password,
		Database: c.Database,
		Options:  c.Options,
	})
	if err != nil {
		_ = host.Close()
		return nil, err
	}
	c.pluginConn = host
	c.pluginConnID = connID
	return host.Bind(connID, c.Database), nil
}
