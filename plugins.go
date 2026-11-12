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
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dbcore/dbcore"
	"tablepro-go/internal/pluginreg"
)

// pluginSpec describes how to launch one engine's plugin.
type pluginSpec struct {
	// Path is the executable. A bare name is looked up on PATH.
	Path string
	// Kind is the engine the plugin claims; the banner must agree.
	Kind string
	// Args are passed to the executable after the path. W17: one console-plugin binary
	// serves several engines and picks one by its first argument, which is what the
	// documented "path;arg;arg" form exists for — and what was documented but never
	// actually parsed or launched.
	Args []string
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
//
// W10: every declaration is verified BEFORE it is registered, so a trampered
// executable is never offered as a driver. The check is deliberately here rather
// than at launch: registering a plugin the trust gate refuses would leave the UI
// offering an engine whose first query fails with a confusing error, and the
// user would have no way to tell "the plugin is broken" from "the plugin was
// swapped".
func (r *pluginRegistry) Load() []error {
	var errs []error
	declared := map[string]string{}
	r.mu.Lock()
	defer r.mu.Unlock()
	trustStore, err := loadPluginTrustStore()
	if err != nil {
		// A trust store that cannot be read is a warning, not a fatal: the app
		// still works without plugins, and refusing to start over it would be a
		// worse failure. Every plugin then lands in the "unknown" bucket, which
		// is visible in the panel.
		errs = append(errs, fmt.Errorf("插件信任清单不可用（本次全部按未信任处理）: %w", err))
		trustStore = &pluginreg.TrustStore{}
	}
	trusted := pluginreg.TrustedMap(trustStore)

	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "TABLEPRO_PLUGIN_") || value == "" {
			continue
		}
		typ := strings.ToLower(strings.TrimPrefix(name, "TABLEPRO_PLUGIN_"))
		// The documented form is "path[;arg;arg]": the arguments after the first
		// semicolon are passed to the executable. Splitting here (rather than stat-ing
		// the whole string) is what makes one binary able to serve several engines —
		// the console plugin picks Milvus/Kafka/Pulsar/Nacos by its first argument.
		parts := strings.Split(value, ";")
		// A relative path is resolved now: the child's working directory is the
		// process's, which is not necessarily where the user typed the command.
		path := strings.TrimSpace(parts[0])
		var args []string
		for _, a := range parts[1:] {
			if a = strings.TrimSpace(a); a != "" {
				args = append(args, a)
			}
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if _, err := os.Stat(path); err != nil {
			errs = append(errs, fmt.Errorf("plugin for %s: %w", typ, err))
			continue
		}
		// Two declarations for the same engine type used to collide silently,
		// with the last one winning. Report the conflict instead of picking one
		// (TP-19): a silent last-wins is how a stale plugin shadows a new one.
		if first, dup := declared[typ]; dup {
			errs = append(errs, fmt.Errorf(
				"plugin for %s declared twice: %q and %q (refusing to pick silently)", typ, first, path))
			continue
		}
		check, verr := pluginreg.Verify(path, "", trusted, typ)
		if verr != nil {
			errs = append(errs, fmt.Errorf("plugin for %s: %w", typ, verr))
			continue
		}
		if !check.OK() {
			// Tampered: refuse. This is the one state the store exists for.
			errs = append(errs, fmt.Errorf("plugin for %s 被拒：%s", typ, check.Detail()))
			continue
		}
		if check.State == pluginreg.StateUnknown {
			log.Printf("plugin for %s: %s", typ, check.Detail())
		}
		declared[typ] = path
		r.specs[typ] = pluginSpec{Path: path, Kind: typ, Args: args}
	}
	return errs
}

// loadPluginTrustStore reads the trust store from the user data directory.
func loadPluginTrustStore() (*pluginreg.TrustStore, error) {
	path := userDataFilePath("plugin_trust.json")
	store, err := pluginreg.LoadTrustStore(path)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// savePluginTrustStore writes the trust store atomically.
func savePluginTrustStore(store *pluginreg.TrustStore) error {
	return pluginreg.SaveTrustStore(userDataFilePath("plugin_trust.json"), store)
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

// unregister drops one registration. Test isolation: the registry is
// process-global and Go runs tests in one process, so a test that overrides
// a built-in engine type must clean up or every later test in the binary
// sees the override.
func (r *pluginRegistry) unregister(typ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.specs, typ)
}

// startPlugin is the connection-scoped entry point for a plugin-backed engine.
// It routes through startPluginConn, the registry path, so there is exactly one
// way to acquire a plugin process. The old direct plugindriver.Start here
// bypassed pluginHosts' reference counting and had no inverse in the registry
// (TP-17).
func (c *Connection) startPlugin(spec pluginSpec) (dbcore.SQLDriver, error) {
	return c.startPluginConn(spec)
}

// pluginDriver returns the connection's plugin driver, starting it on first use.
// The connection-oriented host is used: one plugin process serves this and any
// other connection of the same engine, so a second tab does not spawn a child.
func (a *App) pluginDriver(conn *Connection) (dbcore.SQLDriver, error) {
	spec, ok := plugins.lookup(conn.Type)
	if !ok {
		return nil, fmt.Errorf("%w: no plugin is registered for %q", dbcore.ErrNotSupported, conn.Type)
	}
	drv, err := conn.startPluginConn(spec)
	if err != nil {
		return nil, err
	}
	if conn.pluginDead() {
		// The child died (crash, OOM kill, an external kill). Its driver would
		// answer every later call with an error and the connection would look
		// healthy forever, so drop the dead handle and reopen on a fresh
		// process (TP-15).
		log.Printf("plugin %s: process died; reopening %s", conn.Type, conn.Name)
		conn.closePlugin()
		drv, err = conn.startPluginConn(spec)
		if err != nil {
			return nil, err
		}
	}
	return drv, nil
}

// pluginDead reports whether this connection's plugin process has died since it
// was opened. It is the lazy-restart probe: the child is shared across
// connections of one engine, so a dead host is only known by asking it.
func (c *Connection) pluginDead() bool {
	c.pluginMu.Lock()
	defer c.pluginMu.Unlock()
	if c.pluginConn != nil {
		return c.pluginConn.Dead()
	}
	if c.plugin != nil {
		if d, ok := c.plugin.(interface{ Dead() bool }); ok {
			return d.Dead()
		}
	}
	return false
}

// closePlugin shuts the child process down; called from Disconnect. The
// connection-oriented connection is dropped first (so the process keeps serving
// any other connection), then the host itself when nothing remains.
func (c *Connection) closePlugin() {
	c.pluginMu.Lock()
	drv := c.plugin
	host := c.pluginConn
	connID := c.pluginConnID
	pluginPath := c.pluginPath
	pluginKind := c.pluginKind
	pluginArgs := c.pluginArgs
	c.plugin = nil
	c.pluginConn = nil
	c.pluginConnID = ""
	c.pluginPath = ""
	c.pluginKind = ""
	c.pluginArgs = nil
	c.pluginMu.Unlock()
	if drv != nil {
		if err := drv.Close(); err != nil {
			log.Printf("close plugin %s: driver: %v", pluginPath, err)
		}
	}
	if host != nil {
		if connID != "" {
			if err := host.CloseConn(context.Background(), connID); err != nil {
				log.Printf("close plugin %s: connection %s: %v", pluginPath, connID, err)
			}
		}
		// Release the host reference: the process is shared across connections
		// of the same engine via pluginHosts, so it is only closed when the
		// last connection of that engine goes away.
		pluginHosts.releaseHost(pluginPath, pluginKind, pluginArgs)
	}
}

// keepaliveInterval is how often the keepalive goroutine pings the server.
// 30s is frequent enough to detect half-open sockets before the next statement,
// but not so frequent that it adds measurable load.
const keepaliveInterval = 30 * time.Second

// startKeepalive launches a goroutine that pings the server periodically.
// The driver is snapshotted into locals here: the goroutine outlives the
// connection's lock, and reads of c.DB/c.KV after that would race the
// connect/disconnect writes that run under connectionsMu. Keepalive is always
// stopped before the driver is closed (Connect tears down the replaced
// connection; Disconnect/Shutdown tear down the live one), so the snapshot is
// never pinged after Close.
func (c *Connection) startKeepalive() {
	db, kv := c.DB, c.KV
	if db == nil && kv == nil {
		return
	}
	stop := make(chan struct{})
	c.keepaliveStop = stop
	go func() {
		ticker := time.NewTicker(keepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				var err error
				if db != nil {
					err = db.PingContext(ctx)
				} else if kv != nil {
					err = kv.Ping(ctx)
				}
				cancel()
				if err != nil {
					log.Printf("keepalive: connection %q ping failed: %v", c.ID, err)
				}
			}
		}
	}()
}

// stopKeepalive signals the keepalive goroutine to stop. Safe to call when no
// goroutine is running. Callers must hold connectionsMu: the field is written
// under that lock by startKeepalive.
func (c *Connection) stopKeepalive() {
	if c.keepaliveStop != nil {
		close(c.keepaliveStop)
		c.keepaliveStop = nil
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
