package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"

	dbcoremssql "github.com/dbcore/dbcore/mssql"
	dbcoremysql "github.com/dbcore/dbcore/mysql"
	dbcoreoracle "github.com/dbcore/dbcore/oracle"
	dbcorepg "github.com/dbcore/dbcore/postgres"
	dbcoresqlite "github.com/dbcore/dbcore/sqlite"
	"tablepro-go/internal/catalogsnap"
	"tablepro-go/internal/pool"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"
)

type Connection struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Host     string            `json:"host"`
	Port     int               `json:"port"`
	Username string            `json:"username"`
	Password string            `json:"password"`
	Database string            `json:"database"`
	SSLMode  string            `json:"sslMode"`
	Options  map[string]string `json:"options"`
	SSH      SSHConfig         `json:"ssh"`
	DB       *sql.DB           `json:"-"`
	// KV is set instead of DB for key/value engines. Selecting a logical
	// database is connection state in Redis, so kvMu keeps one connection's
	// reads on the database they asked for even when two panels run at once.
	KV     dbcore.KVDriver  `json:"-"`
	Config ConnectionConfig `json:"-"`

	// connected is set when Connect stores the connection in the map and
	// cleared when Disconnect replaces it with a disconnectedHandle. It
	// distinguishes a live connection from a handle that only carries the
	// saved config, so isConnected does not report a plugin engine as live
	// just because its type is still registered after the child was torn down.
	connected bool

	// tunnel is non-nil when the connection is routed through an SSH jump host;
	// dialHost/dialPort then point at its loopback listener.
	tunnel    *sshTunnel `json:"-"`
	dialHost  string     `json:"-"`
	dialPort  int        `json:"-"`
	kvMu      sync.Mutex
	kvDefault int
	kvDB      int
	kvDBSet   bool

	// plugin is the child process serving this connection when the engine is not
	// compiled in (plugins.go). Its driver carries metadata and statements, so DB
	// stays nil for such a connection.
	//
	// pluginConn is the same process in connection-oriented mode: one plugin
	// serving several connections, the way a browser-style host does (see
	// plugins_resolve.go). Only one of the two is used per connection.
	//
	// pluginConnID is the id this connection opened on pluginConn, so its driver
	// (pluginConn.Bind) can be recovered after a reload and closed on disconnect.
	pluginMu     sync.Mutex
	plugin       dbcore.SQLDriver
	pluginConn   *plugindriver.ConnDriver
	pluginConnID string
	// pluginPath is the executable path of the plugin process this connection
	// uses, so closePlugin can release the shared-host reference by key.
	pluginPath string
	// pluginKind is the engine the plugin process serves; together with
	// pluginPath it forms the registry key (hostKey). closePlugin needs it to
	// release the exact entry acquireHost used.
	pluginKind string

	// keepaliveStop signals the keepalive goroutine to stop. Nil means no
	// goroutine is running. The keepalive pings the server periodically so
	// half-open sockets are detected before the next statement would fail.
	keepaliveStop chan struct{}

	// pools caches one *sql.DB per database (internal/pool); poolsOnce builds it
	// on first use so a connection that never browses another database pays
	// nothing. poolsMu serializes creation (ensurePools, called from query
	// paths outside connectionsMu) against teardown (closePools, called under
	// connectionsMu), so the field is never written and read concurrently.
	// poolsClosed is set by closePools; poolSet then returns nil so a stale
	// reader that still holds the *Connection cannot re-open a pool on a
	// connection the user just disconnected.
	poolsMu     sync.Mutex
	poolsOnce   sync.Once
	pools       *pool.Set
	poolsClosed bool

	// catalog caches this connection's whole object list for quick-open,
	// rebuilt only when App.catalogVersion moves past its generation
	// (internal/catalogsnap).
	catalog catalogsnap.Cache
}

type ConnectionConfig struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
	SSLMode  string `json:"sslMode"`
	// IsolationLevel optionally sets the transaction isolation level for this
	// connection's transactions (read_uncommitted/read_committed/
	// repeatable_read/serializable/""). Empty means the engine's default.
	IsolationLevel string `json:"isolationLevel,omitempty"`
	// Group labels the sidebar section the connection renders under (P2-3);
	// "" is the ungrouped section, so old connection files keep their layout.
	Group   string            `json:"group,omitempty"`
	Options map[string]string `json:"options"`
	// SSH optionally routes the connection through a jump host. The database
	// host/port above stay the ones the user typed; the tunnel rewrites only the
	// dialed endpoint (see dialTarget).
	SSH SSHConfig `json:"ssh"`

	DatabaseFilter DatabaseFilter `json:"databaseFilter"`
}

// DatabaseFilter hides databases in the navigator. Patterns are SQL-LIKE style
// masks where "%" and "*" stand for any run of characters and "_" for one.
// Patterns themselves switch the filter on: with nothing listed everything
// shows, so a half-filled dialog can never look like it did nothing.
type DatabaseFilter struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

func (f DatabaseFilter) allows(name string) bool {
	if len(f.Include) == 0 && len(f.Exclude) == 0 {
		return true
	}
	for _, pattern := range f.Exclude {
		if matchMask(pattern, name) {
			return false
		}
	}
	if len(f.Include) == 0 {
		return true
	}
	for _, pattern := range f.Include {
		if matchMask(pattern, name) {
			return true
		}
	}
	return false
}

func matchMask(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	var sb strings.Builder
	sb.WriteString(`(?i)\A`)
	for _, r := range pattern {
		switch r {
		case '%', '*':
			sb.WriteString(`.*`)
		case '_':
			sb.WriteString(`.`)
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	sb.WriteString(`\z`)
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

func (a *App) TestConnection(config ConnectionConfig) error {
	log.Printf("Testing connection: type=%s host=%s:%d db=%s user=%s encrypt=%s ssh=%v",
		config.Type, config.Host, config.Port, config.Database, config.Username, config.SSLMode, config.SSH.Enabled)

	host, port := config.Host, config.Port
	if config.SSH.Enabled {
		if config.Type == "sqlite" {
			return fmt.Errorf("ssh: SQLite is a local file and cannot use a tunnel")
		}
		tun, localHost, localPort, err := openSSHTunnel(config.SSH, config.Host, config.Port)
		if err != nil {
			return fmt.Errorf("ssh tunnel failed: %w", err)
		}
		defer tun.Close()
		host, port = localHost, localPort
	}

	if spec, isPlugin := plugins.lookup(config.Type); isPlugin {
		// Test eagerly starts the plugin child and shuts it down: "test"
		// means a bad path or a plugin that will not run surfaces HERE, not
		// at the first query after the UI already said connected.
		host, err := plugindriver.StartConn(context.Background(), spec.Path, nil)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		if string(host.Kind()) != spec.Kind && spec.Kind != "" {
			_ = host.Close()
			return fmt.Errorf("plugin %s speaks %q, but it is registered as %q", spec.Path, host.Kind(), spec.Kind)
		}
		_ = host.Close()
		return nil
	}
	if config.Type == "redis" {
		kvConfig := config
		kvConfig.Host, kvConfig.Port = host, port
		drv, err := openKV(kvConfig)
		if err != nil {
			log.Printf("openKV error: %v", err)
			return fmt.Errorf("connection failed: %w", err)
		}
		defer drv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := drv.Ping(ctx); err != nil {
			log.Printf("redis ping error: %v", err)
			return fmt.Errorf("connection failed: %w", err)
		}
		return nil
	}
	db, err := createConnectionAt(config, host, port)
	if err != nil {
		log.Printf("createConnection error: %v", err)
		return fmt.Errorf("connection failed: %w", err)
	}
	defer db.Close()
	pctx, cancel := context.WithTimeout(context.Background(), connectPingTimeout)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		log.Printf("db.Ping error: %v", err)
		return fmt.Errorf("connection failed: %w", err)
	}
	return nil
}

func (a *App) GetConnections() []ConnectionConfig {
	a.connectionsMu.RLock()
	defer a.connectionsMu.RUnlock()
	result := make([]ConnectionConfig, len(a.savedConnections))
	for i, c := range a.savedConnections {
		clean := c
		clean.Password = ""
		// The SSH password is a credential too: never send it back to the UI.
		// UpdateConnection restores it when the field comes back blank.
		clean.SSH.Password = ""
		// The key passphrase is the third credential in the same struct; it
		// takes the same route (blanked here, preserved on a blank update).
		clean.SSH.Passphrase = ""
		result[i] = clean
	}
	return result
}

func (a *App) AddConnection(config ConnectionConfig) error {
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()

	id, err := newSecretID()
	if err != nil {
		return fmt.Errorf("generate connection id: %w", err)
	}
	config.ID = id
	a.savedConnections = append(a.savedConnections, config)
	a.saveToFile()
	return nil
}

func (a *App) UpdateConnection(config ConnectionConfig) error {
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()

	for i, c := range a.savedConnections {
		if c.ID == config.ID {
			if config.Password == "" {
				config.Password = c.Password
			}
			if config.SSH.Password == "" {
				config.SSH.Password = c.SSH.Password
			}
			// Blank means "keep the stored one" for the key passphrase too:
			// GetConnections blanks it, so a dialog save always comes back
			// empty and overwriting would silently drop the credential.
			if config.SSH.Passphrase == "" {
				config.SSH.Passphrase = c.SSH.Passphrase
			}
			a.savedConnections[i] = config
			a.invalidateConnFilterCacheLocked()
			a.saveToFile()
			return nil
		}
	}
	return fmt.Errorf("connection not found: %s", config.ID)
}

func (a *App) DeleteConnection(id string) error {
	// The teardown runs OUTSIDE the map lock: DB.Close drains the pool set
	// (up to CloseGrace 30s with a stalled statement in flight) and
	// closePlugin waits on the plugin process's polite-close window. Holding
	// connectionsMu through them queued every Connect/Disconnect/list
	// operation behind one slow disconnect — the same shape the Connect dial
	// moving outside the lock already fixed. The connection is first evicted
	// from the map under the lock (so nothing new reaches it), then closed
	// lock-free.
	a.connectionsMu.Lock()
	conn, found := a.connections[id]
	var index = -1
	for i, c := range a.savedConnections {
		if c.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		a.connectionsMu.Unlock()
		return fmt.Errorf("connection not found: %s", id)
	}
	if found {
		conn.stopKeepalive()
		delete(a.connections, id)
	}
	a.savedConnections = append(a.savedConnections[:index], a.savedConnections[index+1:]...)
	a.saveToFile()
	a.connectionsMu.Unlock()

	if found {
		conn.closeAllHandles(id)
	}
	return nil
}

// close tears down every live resource of this connection in LIFO order. It is
// the single inverse of Connect, so the inverse cannot be forgotten at a call
// site.
//
// The on-demand acquisitions (tab transaction managers, per-database pools, the
// plugin child) are released before the primary DB/KV driver, and a server-side
// transaction is rolled back BEFORE its driver is closed: a plugin engine's
// transaction lives in the child process, so rolling back after closePlugin
// hits a dead driver and leaves the server-side transaction orphaned (TP-02).
//
// Callers run it OUTSIDE connectionsMu — it can block on pool drains and the
// plugin's close window. Eviction from the connections map must happen first
// (under the lock), so nothing new reaches the connection while this runs;
// readers that already hold the *Connection see closed handles, which is the
// same outcome Disconnect's in-place teardown produces.
func (c *Connection) close(connID string) {
	c.stopKeepalive()
	closeTxns(connID)
	c.closePools()
	c.closePlugin()
	if c.DB != nil {
		if err := c.DB.Close(); err != nil {
			// The inverse of an acquisition must be loud when it fails: a
			// silently leaked handle keeps a server connection alive after the
			// client believes it disconnected (TP-25).
			log.Printf("close %s: database handle: %v", connID, err)
		}
	}
	c.closeKV()
	c.tunnel.Close()
}

// closeAllHandles is the DeleteConnection-facing form of close.
func (c *Connection) closeAllHandles(connID string) {
	c.close(connID)
}

// dialTarget returns the endpoint the drivers should dial: the tunnel's loopback
// listener when SSH is active, otherwise the configured host/port.
func (c *Connection) dialTarget() (string, int) {
	if c.tunnel != nil && c.dialHost != "" {
		return c.dialHost, c.dialPort
	}
	return c.Host, c.Port
}

func (a *App) Connect(id string) error {
	// Snapshot the config and open everything OUTSIDE the map lock: the SSH
	// dial plus the 10s reachability ping used to hold the write lock, and
	// every RLock'd API (connection(), GetConnections, SearchObjects…)
	// froze for the whole round trip. The commit at the bottom re-takes the
	// lock and tears down whatever previous connection this replaces.
	a.connectionsMu.RLock()
	var found *ConnectionConfig
	for i := range a.savedConnections {
		if a.savedConnections[i].ID == id {
			found = &a.savedConnections[i]
			break
		}
	}
	// Value copy: the pointer would be dereferenced for the whole dial below,
	// after RUnlock — UpdateConnection writes the same element in place and
	// DeleteConnection shifts the slice, so the snapshot must not alias it.
	var configVal ConnectionConfig
	if found != nil {
		configVal = *found
	}
	a.connectionsMu.RUnlock()
	var config *ConnectionConfig
	if found != nil {
		config = &configVal
	}
	if config == nil {
		return fmt.Errorf("invalid connection id: %s", id)
	}

	conn := &Connection{
		ID:       id,
		Name:     config.Name,
		Type:     config.Type,
		Host:     config.Host,
		Port:     config.Port,
		Username: config.Username,
		Password: config.Password,
		Database: config.Database,
		SSLMode:  config.SSLMode,
		Options:  config.Options,
		SSH:      config.SSH,
		Config:   *config,
	}
	if config.SSH.Enabled {
		if config.Type == "sqlite" {
			return fmt.Errorf("ssh: SQLite is a local file and cannot use a tunnel")
		}
		if _, isPlugin := plugins.lookup(config.Type); isPlugin {
			return fmt.Errorf("ssh: 插件引擎自带连接逻辑，隧道目标无法传达给它")
		}
		tun, host, port, err := openSSHTunnel(config.SSH, config.Host, config.Port)
		if err != nil {
			return fmt.Errorf("ssh tunnel failed: %w", err)
		}
		conn.tunnel, conn.dialHost, conn.dialPort = tun, host, port
	}
	if config.Type == "redis" {
		kvConfig := *config
		kvConfig.Host, kvConfig.Port = conn.dialTarget()
		drv, err := openKV(kvConfig)
		if err != nil {
			conn.tunnel.Close()
			return fmt.Errorf("connection failed: %w", err)
		}
		pctx, pcancel := context.WithTimeout(context.Background(), connectPingTimeout)
		pingErr := drv.Ping(pctx)
		pcancel()
		if pingErr != nil {
			drv.Close()
			conn.tunnel.Close()
			return fmt.Errorf("connection failed: %w", pingErr)
		}
		conn.KV = drv
		conn.kvDefault, _ = redisDBIndex(config.Database)
		conn.kvDB = conn.kvDefault
		conn.kvDBSet = true
	} else if _, isPlugin := plugins.lookup(config.Type); isPlugin {
		// Plugin engines dial nothing at connect: the child process starts
		// lazily on first use (isConnected deliberately checks the
		// registration, not the running process — requiring it here would
		// race the very call that starts it), and the plugin owns the engine
		// coordinates. conn.DB stays nil; pluginConnection() keys on that.
	} else {
		host, port := conn.dialTarget()
		db, err := createConnectionAt(*config, host, port)
		if err != nil {
			conn.tunnel.Close()
			return fmt.Errorf("connection failed: %w", err)
		}
		// sql.Open is lazy: without a ping, wrong credentials or a dead host
		// would only surface on the first query, after the UI already showed
		// "connected". Bounded so a black-holed address fails here, not at the
		// OS-level dial timeout.
		pctx, pcancel := context.WithTimeout(context.Background(), connectPingTimeout)
		pingErr := db.PingContext(pctx)
		pcancel()
		if pingErr != nil {
			db.Close()
			conn.tunnel.Close()
			return fmt.Errorf("connection failed: %w", pingErr)
		}
		conn.DB = db
	}

	// Commit: take the write lock and tear down whatever previous
	// connection this replaces — same reasons as Disconnect (a manager left
	// behind would fail its next statement with "database is closed"). The
	// tab sessions go too: their transaction rows were rolled back by the
	// manager swap, and keeping them Active made the next statement fail
	// with "transaction not found" and the session badge lie.
	for _, tabID := range a.forgetSessionTabsFor(id) {
		a.forgetSession(tabID)
	}
	a.connectionsMu.Lock()
	// A concurrent DeleteConnection may have removed the id while the dial
	// ran: committing here would resurrect a ghost connection the UI can no
	// longer reach — drop our dial instead.
	stillWanted := false
	for i := range a.savedConnections {
		if a.savedConnections[i].ID == id {
			stillWanted = true
			break
		}
	}
	if !stillWanted {
		// The connection was deleted while the dial ran: release exactly what
		// this call acquired. Routing through close() (rather than the partial
		// list the ghost path used to spell out) means the pools and the tab
		// transactions are reclaimed here too (TP-03).
		conn.close(id)
		a.connectionsMu.Unlock()
		return nil
	}
	if existing, ok := a.connections[id]; ok {
		existing.close(id)
	}
	a.connections[id] = conn
	conn.connected = true
	a.currentConnID = id
	// Start keepalive while the write lock is still held: it snapshots c.DB /
	// c.KV and writes c.keepaliveStop, both of which must not race a concurrent
	// Disconnect that tears this connection down under the same lock.
	conn.startKeepalive()
	a.connectionsMu.Unlock()

	return nil
}

func (a *App) Disconnect(connID string) error {
	// Roll back and forget this connection's tab sessions while the pools are
	// still alive: after teardown a rollback just fails, and the stale
	// entries made post-reconnect statements fail with "transaction not
	// found". Runs before connectionsMu — forgetSession reads the connection
	// map through it.
	for _, tabID := range a.forgetSessionTabsFor(connID) {
		a.forgetSession(tabID)
	}
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()

	if conn, ok := a.connections[connID]; ok {
		// Release the connection's whole acquisition set in LIFO order via the
		// single inverse (TP-01): stop the keepalive, roll back the tab
		// transactions, close the per-database pools, then the plugin child,
		// then the primary driver and the tunnel.
		conn.close(connID)
		// Copy-on-write the handle instead of nilling fields in place. Other
		// goroutines may still hold this *Connection from an earlier lookup and
		// read conn.DB/conn.KV under no lock, so the old object must become
		// immutable. The map entry survives (lookups report "not connected"),
		// which is the contract DisconnectAll's route test pins.
		a.connections[connID] = disconnectedHandle(conn)
	}
	if a.currentConnID == connID {
		a.currentConnID = ""
	}
	return nil
}

// disconnectedHandle is the published replacement for a connection whose
// resources were just closed. It carries the connection's identity and saved
// config (so the sidebar still renders it and lookups report "not connected")
// but no live DB/KV/pool/plugin handle. Building a fresh object - rather than
// nilling fields on the old one - is what makes teardown race-free: readers
// that already fetched the old *Connection keep reading fields nobody writes
// after this point.
func disconnectedHandle(c *Connection) *Connection {
	return &Connection{
		ID:       c.ID,
		Name:     c.Name,
		Type:     c.Type,
		Host:     c.Host,
		Port:     c.Port,
		Username: c.Username,
		Password: c.Password,
		Database: c.Database,
		SSLMode:  c.SSLMode,
		Options:  c.Options,
		SSH:      c.SSH,
		Config:   c.Config,
	}
}

// DisconnectAll closes every open connection. The idle lock calls it (P2-9):
// a locked app must not keep live database sessions behind the lock screen,
// otherwise the credential-free window is still a doorway into the data.
func (a *App) DisconnectAll() {
	a.connectionsMu.RLock()
	ids := make([]string, 0, len(a.connections))
	for id := range a.connections {
		ids = append(ids, id)
	}
	a.connectionsMu.RUnlock()
	for _, id := range ids {
		_ = a.Disconnect(id)
	}
}

// isConnected reports whether this connection can answer a call. Three shapes count:
// a database pool, a key/value driver, or an engine served by a registered plugin
// (plugins.go). The plugin case deliberately checks the registration rather than the
// running process: the child is started lazily on first use, so requiring a running
// process here would deadlock the call that would have started it. A plugin engine
// has no DB/KV handle until the child starts, so the connected flag (set by Connect,
// cleared by Disconnect's disconnectedHandle) distinguishes a live plugin connection
// from a handle that only carries the registered type.
func (c *Connection) isConnected() bool {
	if c == nil {
		return false
	}
	if c.DB != nil || c.KV != nil {
		return true
	}
	if !c.connected {
		return false
	}
	return pluginConnection(c)
}

func (a *App) connection(connID string) (*Connection, error) {
	a.connectionsMu.RLock()
	defer a.connectionsMu.RUnlock()

	conn, ok := a.connections[connID]
	if !ok {
		return nil, fmt.Errorf("not connected: no connection with id %q", connID)
	}
	if !conn.isConnected() {
		// The engine is named: the user needs to know *which* connection is not
		// usable, and whether it is a plugin that is missing.
		if _, ok := plugins.lookup(conn.Type); ok {
			return nil, fmt.Errorf("not connected: the %q plugin is registered but the connection was never opened", conn.Type)
		}
		return nil, fmt.Errorf("not connected: %s (%s)", conn.Name, conn.Type)
	}
	return conn, nil
}

// liveConnection accepts either engine: catalog entries that exist for both
// SQL and key/value connections branch on Type after the lookup.
func (a *App) liveConnection(connID string) (*Connection, error) {
	a.connectionsMu.RLock()
	defer a.connectionsMu.RUnlock()

	conn, ok := a.connections[connID]
	if !ok {
		return nil, fmt.Errorf("not connected: no connection with id %q", connID)
	}
	if !conn.isConnected() {
		if _, ok := plugins.lookup(conn.Type); ok {
			return nil, fmt.Errorf("not connected: the %q plugin is registered but the connection was never opened", conn.Type)
		}
		return nil, fmt.Errorf("not connected: %s (%s)", conn.Name, conn.Type)
	}
	return conn, nil
}

func (a *App) kvConnection(connID string) (*Connection, error) {
	a.connectionsMu.RLock()
	defer a.connectionsMu.RUnlock()

	conn, ok := a.connections[connID]
	if !ok || conn.KV == nil {
		return nil, fmt.Errorf("not connected")
	}
	return conn, nil
}

// Connection-pool bounds. Without these database/sql defaults to unlimited
// open connections, so a fan-out (per-database metadata browse, concurrent
// grid requests) could open one server-side connection per goroutine.
// Values mirror the conservative caps the reference desktop clients use.
// ConnMaxIdleTime reaps connections that sit idle past it: servers kill idle
// sessions on their own schedule (MySQL wait_timeout, PG idle reaper), and a
// reaped connection only surfaces as a transport error on the next statement —
// retiring it client-side first keeps the pool's sessions fresh. It must stay
// shorter than ConnMaxLifetime or it would never be the binding constraint.
const (
	dbMaxOpenConns        = 10
	dbMaxIdleConns        = 5
	dbConnMaxLifetime     = 30 * time.Minute
	dbConnMaxIdleTime     = 5 * time.Minute
	maxPoolsPerConnection = 32
)

// connSpec maps a saved connection onto the shared kernel's ConnSpec so the DSN
// text for every engine comes from dbcore instead of five hand-written format
// strings. That also fixes SSLMode spellings: the UI stores "true"/"false" while
// lib/pq only accepts disable/require/verify-*, and dbcore's mapping translates.
func connSpec(config ConnectionConfig) dbcore.ConnSpec {
	return connSpecAt(config, config.Host, config.Port)
}

// connSpecAt is connSpec with the dialed endpoint overridden (SSH tunnel).
func connSpecAt(config ConnectionConfig, host string, port int) dbcore.ConnSpec {
	kind := dbcore.DatabaseKind(config.Type)
	switch builtinEngine(config.Type) {
	case "postgresql":
		kind = dbcore.KindPostgres
	case "sqlserver":
		kind = dbcore.KindMSSQL
	case "sqlite":
		kind = dbcore.KindSQLite
	case "mysql":
		kind = dbcore.KindMySQL
	case "oracle":
		kind = dbcore.KindOracle
	}
	return dbcore.ConnSpec{
		Kind:     kind,
		Host:     host,
		Port:     port,
		User:     config.Username,
		Password: config.Password,
		Database: config.Database, // MySQL database / PG db / MSSQL db / Oracle service
		File:     config.Database, // SQLite: Database holds the file path
		SSLMode:  config.SSLMode,
	}
}

func createConnection(config ConnectionConfig) (*sql.DB, error) {
	return createConnectionAt(config, config.Host, engineDefaultPortFor(config))
}

// createConnectionAt opens the engine at an explicit host/port. The SSH tunnel
// passes its loopback endpoint here, so the DSN builders never need to know a
// tunnel exists.
// connectPingTimeout bounds the reachability check in Connect and
// TestConnection: a black-holed address must fail with a real error instead of
// hanging until the OS gives up on the dial.
const connectPingTimeout = 10 * time.Second

func createConnectionAt(config ConnectionConfig, host string, port int) (*sql.DB, error) {
	spec := connSpecAt(config, host, port)
	var dsn string
	var driver string
	switch builtinEngine(config.Type) {
	case "mysql":
		driver = "mysql"
		dsn = dbcoremysql.DSN(spec)
	case "postgresql":
		driver = "postgres"
		dsn = dbcorepg.DSN(spec)
	case "sqlite":
		driver = "sqlite3"
		dsn = dbcoresqlite.DSN(spec)
		// The kernel DSN is the bare file path; mattn/go-sqlite3 (the driver
		// this binary registers) takes its pragmas as DSN parameters, which
		// run on EVERY physical connection the pool opens. Exec-ing them once
		// here only touched the first connection — the pool's secondary
		// connections kept busy_timeout=0 and concurrent writes failed with
		// SQLITE_BUSY immediately.
		if config.Database != "" && config.Database != ":memory:" {
			dsn += "?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL"
		}
	case "sqlserver":
		driver = "sqlserver"
		dsn = dbcoremssql.DSN(spec)
	case "oracle":
		driver = "oracle"
		dsn = dbcoreoracle.DSN(spec)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", config.Type)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(dbMaxOpenConns)
	db.SetMaxIdleConns(dbMaxIdleConns)
	// SQLite `:memory:` is per-connection: every pool connection would see its
	// own separate empty database, so a concurrent query could land on a fresh
	// handle and observe a table that was just created on another. Pin the pool
	// to a single connection (polydb's dbsqlite driver does the same).
	if config.Type == "sqlite" && (config.Database == "" || config.Database == ":memory:") {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	db.SetConnMaxLifetime(dbConnMaxLifetime)
	db.SetConnMaxIdleTime(dbConnMaxIdleTime)
	return db, nil
}

// engineDefaultPortFor 是 engines.go 的端口表在 ConnectionConfig 上的入口:
// 端口未填 (0) 时回落到该引擎的常规端口, 让OceanBase(2881)/StarRocks(9030) 等
// 别名与底层引擎一样开箱即用。
func engineDefaultPortFor(config ConnectionConfig) int {
	if config.Port != 0 {
		return config.Port
	}
	if p := engineDefaultPort(config.Type); p != 0 {
		return p
	}
	return builtinEngineDefaultPort(config.Type)
}

// builtinEngineDefaultPort is the port a built-in engine conventionally listens
// on; aliases that do not declare their own fall through to their base engine.
func builtinEngineDefaultPort(engineType string) int {
	switch builtinEngine(engineType) {
	case "mysql":
		return 3306
	case "postgresql":
		return 5432
	case "sqlserver":
		return 1433
	case "oracle":
		return 1521
	case "sqlite":
		return 0
	}
	return 0
}
