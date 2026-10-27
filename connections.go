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

	// pools caches one *sql.DB per database (internal/pool); poolsOnce builds it
	// on first use so a connection that never browses another database pays
	// nothing.
	poolsOnce sync.Once
	pools     *pool.Set

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
			a.savedConnections[i] = config
			a.invalidateConnFilterCacheLocked()
			a.saveToFile()
			return nil
		}
	}
	return fmt.Errorf("connection not found: %s", config.ID)
}

func (a *App) DeleteConnection(id string) error {
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()

	for i, c := range a.savedConnections {
		if c.ID == id {
			if conn, ok := a.connections[id]; ok {
				if conn.DB != nil {
					conn.DB.Close()
				}
				conn.closeKV()
				conn.closePools()
				conn.closePlugin()
				closeTxns(id)
				conn.tunnel.Close()
				delete(a.connections, id)
			}
			a.savedConnections = append(a.savedConnections[:i], a.savedConnections[i+1:]...)
			a.saveToFile()
			return nil
		}
	}
	return fmt.Errorf("connection not found: %s", id)
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
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()

	var config *ConnectionConfig
	for i := range a.savedConnections {
		if a.savedConnections[i].ID == id {
			config = &a.savedConnections[i]
			break
		}
	}
	if config == nil {
		return fmt.Errorf("invalid connection id: %s", id)
	}

	if existing, ok := a.connections[id]; ok {
		if existing.DB != nil {
			existing.DB.Close()
		}
		existing.closeKV()
		existing.closePools()
		existing.closePlugin()
		// Same reason as Disconnect: a manager left behind would fail its next
		// statement with "database is closed" against the new connection.
		closeTxns(id)
		existing.tunnel.Close()
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

	a.connections[id] = conn
	a.currentConnID = id

	return nil
}

func (a *App) Disconnect(connID string) error {
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()

	if conn, ok := a.connections[connID]; ok {
		if conn.DB != nil {
			conn.DB.Close()
			conn.DB = nil
		}
		conn.closeKV()
		conn.closePools()
		conn.closePlugin()
		// A manager wraps a driver built from the DB just closed; leaving it
		// registered would make the next statement fail with "database is closed".
		closeTxns(connID)
		conn.tunnel.Close()
	}
	if a.currentConnID == connID {
		a.currentConnID = ""
	}
	return nil
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
// process here would deadlock the call that would have started it.
func (c *Connection) isConnected() bool {
	if c == nil {
		return false
	}
	if c.DB != nil || c.KV != nil {
		return true
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
const (
	dbMaxOpenConns        = 10
	dbMaxIdleConns        = 5
	dbConnMaxLifetime     = 30 * time.Minute
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
	switch config.Type {
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
	return createConnectionAt(config, config.Host, config.Port)
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
	switch config.Type {
	case "mysql":
		driver = "mysql"
		dsn = dbcoremysql.DSN(spec)
	case "postgresql":
		driver = "postgres"
		dsn = dbcorepg.DSN(spec)
	case "sqlite":
		driver = "sqlite3"
		dsn = dbcoresqlite.DSN(spec)
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
	db.SetConnMaxLifetime(dbConnMaxLifetime)
	return db, nil
}
