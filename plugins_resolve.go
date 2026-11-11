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
	"sync"

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
// Protocol-compatible aliases (OceanBase MySQL 模式 / openGauss / 金仓 …) ride the
// built-in driver of the engine whose wire protocol they speak.
func hasBuiltinDriver(engineType string) bool {
	switch builtinEngine(engineType) {
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

// pluginHostRegistry keeps one plugin process per plugin path, shared by all
// connections that use it. Without it, every tab of the same plugin engine
// spawns its own child process — and closing one tab's connection kills a
// process that other tabs may still be using. With the registry, the process
// lives until the last connection of that engine goes away.
type pluginHostRegistry struct {
	mu    sync.Mutex
	hosts map[string]*pluginHostEntry
}

type pluginHostEntry struct {
	host     *plugindriver.ConnDriver
	refCount int
	// withdrawing marks an entry whose process is being torn down but which is
	// still recorded, so a concurrent acquireHost waits on done instead of
	// starting a second process for the same key (TP-16).
	withdrawing bool
	done         chan struct{} // closed once host.Close() returned
}

// hostKey identifies a plugin process by executable AND the engine it serves.
// Keying on the path alone collides when one binary serves two engine types
// (TP-29); the NUL separator keeps the composite unambiguous.
func hostKey(path, kind string) string { return path + "\x00" + kind }

var pluginHosts = &pluginHostRegistry{
	hosts: make(map[string]*pluginHostEntry),
}

// acquireHost returns a shared host for the plugin path, incrementing its
// reference count. The caller must call releaseHost when done.
//
// The cold start runs OUTSIDE the registry lock: StartConn waits for the
// child's banner (up to 10s) and a wedged plugin hangs it entirely. Holding
// the lock through it queued every other connection of the same engine —
// including the ones that would have found the warm host on the fast path.
// Two concurrent cold starts for the same path can both run; the loser closes
// its process and joins the winner under the lock.
func (r *pluginHostRegistry) acquireHost(path, kind string) (*plugindriver.ConnDriver, error) {
	key := hostKey(path, kind)
	for {
		r.mu.Lock()
		entry, ok := r.hosts[key]
		if ok {
			if entry.withdrawing {
				// A withdrawal is in flight for this key: wait for the old
				// process to finish closing instead of starting a second one
				// (TP-16).
				done := entry.done
				r.mu.Unlock()
				<-done
				continue
			}
			entry.refCount++
			host := entry.host
			r.mu.Unlock()
			return host, nil
		}
		r.mu.Unlock()

		host, err := plugindriver.StartConn(context.Background(), path, nil)
		if err != nil {
			return nil, err
		}

		r.mu.Lock()
		if winner, wok := r.hosts[key]; wok {
			// A concurrent cold start won the race: join it, retire ours.
			winner.refCount++
			r.mu.Unlock()
			_ = host.Close()
			return winner.host, nil
		}
		r.hosts[key] = &pluginHostEntry{host: host, refCount: 1}
		r.mu.Unlock()
		return host, nil
	}
}

// releaseHost decrements the reference count for the plugin key and closes
// the host when it reaches zero.
func (r *pluginHostRegistry) releaseHost(path, kind string) {
	key := hostKey(path, kind)
	r.mu.Lock()
	entry, ok := r.hosts[key]
	if !ok || entry.withdrawing {
		r.mu.Unlock()
		return
	}
	entry.refCount--
	if entry.refCount > 0 {
		r.mu.Unlock()
		return
	}
	// Mark the entry withdrawing and keep it recorded until the process is
	// actually gone: a concurrent acquireHost then waits on entry.done rather
	// than starting a second process for the same key (TP-16).
	entry.withdrawing = true
	entry.done = make(chan struct{})
	host := entry.host
	r.mu.Unlock()

	_ = host.Close()

	r.mu.Lock()
	delete(r.hosts, key)
	close(entry.done)
	r.mu.Unlock()
}

// startPluginConn launches a plugin in connection-oriented mode and opens the
// connection this Connection represents on it. The plugin process is shared
// across all connections of the same engine via pluginHosts, so a second tab
// does not spawn a second child. The bound driver is returned; it is what every
// catalog and query call goes through.
//
// pluginMu guards the three handle fields only: the acquire/Open work runs
// OUTSIDE it. Open carries the database's own TCP + SSH + handshake latency
// (10s+ on a cold plugin), and holding the per-connection lock through it
// serialized every other call on this Connection — including the Close side,
// whose closePlugin must take the same lock. Re-entry is idempotent: a second
// caller while the first is still opening re-runs the check and either joins
// the finished handle or opens a duplicate connection that CloseConn reclaims
// via the same registry accounting.
func (c *Connection) startPluginConn(spec pluginSpec) (dbcore.SQLDriver, error) {
	c.pluginMu.Lock()
	if c.pluginConn != nil && c.pluginConnID != "" {
		drv := c.pluginConn.Bind(c.pluginConnID, c.Database)
		c.pluginMu.Unlock()
		return drv, nil
	}
	c.pluginMu.Unlock()

	host, err := pluginHosts.acquireHost(spec.Path, spec.Kind)
	if err != nil {
		return nil, err
	}
	if string(host.Kind()) != spec.Kind && spec.Kind != "" {
		pluginHosts.releaseHost(spec.Path, spec.Kind)
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
		pluginHosts.releaseHost(spec.Path, spec.Kind)
		return nil, err
	}

	c.pluginMu.Lock()
	// A concurrent opener may have won: keep one handle, return the other.
	if c.pluginConn != nil && c.pluginConnID != "" {
		c.pluginMu.Unlock()
		_ = host.CloseConn(context.Background(), connID)
		pluginHosts.releaseHost(spec.Path, spec.Kind)
		return c.pluginConn.Bind(c.pluginConnID, c.Database), nil
	}
	c.pluginConn = host
	c.pluginConnID = connID
	c.pluginPath = spec.Path
	c.pluginKind = spec.Kind
	drv := host.Bind(connID, c.Database)
	c.pluginMu.Unlock()
	return drv, nil
}
