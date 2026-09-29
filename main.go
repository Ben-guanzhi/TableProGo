package main

import (
	"tableprosql"

	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	svc := NewApp()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal("Failed to start API server:", err)
	}
	apiPort := listener.Addr().(*net.TCPAddr).Port
	log.Printf("HTTP API server on port %d", apiPort)

	svc.startHTTPServerOnListener(listener)

	wailsApp := application.New(application.Options{
		Name:        "TablePro Go",
		Description: "A fast, native database client built with Go and Wails.",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: &injectHandler{
				inner:  application.AssetFileServerFS(assets),
				inject: fmt.Sprintf("<script>window.__API_PORT__=%d;</script>", apiPort),
			},
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "TablePro Go",
		MinWidth:  800,
		MinHeight: 600,
		Width:     1200,
		Height:    800,
		URL:       "/",
	})

	if err := wailsApp.Run(); err != nil {
		log.Fatal(err)
	}
}

type App struct {
	ctx              context.Context
	connections      map[string]*Connection
	connectionsMu    sync.RWMutex
	currentConnID    string
	savedConnections []ConnectionConfig
	filePath         string
	settings         AppSettings
	settingsPath     string
	settingsMu       sync.RWMutex
	history          []HistoryEntry
	historyPath      string
	historySeq       int64
	historyMu        sync.Mutex
	// histObserved is the history file as this process last read or wrote it;
	// mergeHistory folds in only what other clients added since then.
	histObserved []HistoryEntry
	// connObserved is the connections file as this process last read or wrote
	// it; saveToFile folds in only what other clients added since then.
	connObserved []ConnectionConfig
}

// AppSettings holds UI preferences, persisted next to the connection store.
type AppSettings struct {
	ShowColumnComments bool `json:"showColumnComments"`
	// ResultLimit caps how many rows a SELECT returns; 0 means no cap.
	ResultLimit int `json:"resultLimit"`
	// Theme is "system", "light" or "dark"; the front end resolves "system"
	// against the OS setting, so the backend only stores the choice.
	Theme string `json:"theme"`
}

// defaultResultLimit matches what GUI clients hand out before the user says
// otherwise: enough rows to inspect a table, few enough to stay instant.
const defaultResultLimit = 1000

// defaultTheme follows the OS, which is what every desktop database client
// shipped in recent years does.
const defaultTheme = "system"

func normalizeTheme(theme string) string {
	switch theme {
	case "light", "dark", "system":
		return theme
	}
	return defaultTheme
}

// userDataDir returns where the state files (connections, settings, history)
// live: the user's config directory, not the executable's folder, so saved
// credentials and query history never sit inside a source tree or a shared
// program directory.
func userDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "TableProGo")
	}
	// Without an OS config directory the old beside-the-exe layout is kept
	// rather than refusing to run.
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// userDataFilePath resolves a state file under userDataDir. Releases before
// this layout kept the files beside the executable; such a file is copied
// over once, so an existing installation keeps its saved connections and
// history on first launch. The legacy copy is left in place — deleting user
// data on startup is not this program's call.
func userDataFilePath(name string) string {
	dir := userDataDir()
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); err == nil {
		return target
	}
	exe, err := os.Executable()
	if err != nil {
		return target
	}
	legacy := filepath.Join(filepath.Dir(exe), name)
	data, err := os.ReadFile(legacy)
	if err != nil {
		return target
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return target
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		log.Printf("Failed to migrate %s: %v", legacy, err)
		return target
	}
	log.Printf("Migrated %s to %s", legacy, target)
	return target
}

func connectionsFilePath() string { return userDataFilePath("connections.json") }
func settingsFilePath() string    { return userDataFilePath("settings.json") }
func historyFilePath() string     { return userDataFilePath("history.json") }

// HistoryEntry is one executed statement. The object view's history page and
// later change auditing read it back, so the object the statement targeted is
// stored alongside the text.
type HistoryEntry struct {
	ID       int64  `json:"id"`
	Time     int64  `json:"time"`
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Table    string `json:"table,omitempty"`
	SQL      string `json:"sql"`
	RowCount int    `json:"rowCount"`
	Affected int64  `json:"affectedRows"`
	// ExecutionTime is in seconds, matching QueryResult.
	ExecutionTime float64 `json:"executionTime"`
	Error         string  `json:"error,omitempty"`
}

// historyLimit keeps the on-disk log bounded; a GUI session produces orders of
// magnitude more browses than statements worth remembering.
const historyLimit = 500

// historySQLChars truncates stored statements so one pasted megabyte of SQL
// cannot bloat the file.
const historySQLChars = 4000

func NewApp() *App {
	a := &App{
		connections:  make(map[string]*Connection),
		filePath:     connectionsFilePath(),
		settingsPath: settingsFilePath(),
		historyPath:  historyFilePath(),
	}
	a.loadFromFile()
	a.loadSettings()
	a.loadHistory()
	return a
}

func (a *App) loadSettings() {
	a.settings.ResultLimit = defaultResultLimit
	a.settings.Theme = defaultTheme

	data, err := os.ReadFile(a.settingsPath)
	if err != nil {
		return
	}
	// The limit is read through a pointer so an explicit 0 (no limit) survives,
	// while a file written before the setting existed falls back to the default.
	var stored struct {
		ShowColumnComments bool   `json:"showColumnComments"`
		ResultLimit        *int   `json:"resultLimit"`
		Theme              string `json:"theme"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		log.Printf("Failed to load settings: %v", err)
		return
	}
	a.settings.ShowColumnComments = stored.ShowColumnComments
	if stored.ResultLimit != nil {
		a.settings.ResultLimit = *stored.ResultLimit
	}
	a.settings.Theme = normalizeTheme(stored.Theme)
}

func (a *App) GetSettings() AppSettings {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.settings
}

func (a *App) SaveSettings(settings AppSettings) error {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()

	settings.Theme = normalizeTheme(settings.Theme)

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(a.settingsPath, data, 0600); err != nil {
		return err
	}
	a.settings = settings
	return nil
}

// secretPrefix marks a password in the connection file as system-encrypted.
const secretPrefix = "dpapi:"

func (a *App) loadFromFile() {
	conns, ok, err := readJSONFile[ConnectionConfig](a.filePath)
	if err != nil {
		log.Printf("Failed to load connections: %v", err)
		return
	}
	if !ok {
		return
	}
	needsSealing := false
	for i := range conns {
		if conns[i].Password != "" && !strings.HasPrefix(conns[i].Password, secretPrefix) {
			needsSealing = true
		}
		password, err := unprotectSecret(conns[i].Password)
		if err != nil {
			// A password from another Windows account cannot be opened; keep the
			// connection so its other settings survive, and let the user retype it.
			log.Printf("Connection %s: %v", conns[i].ID, err)
			password = ""
		}
		conns[i].Password = password
	}
	a.savedConnections = conns
	a.connObserved = append([]ConnectionConfig(nil), conns...)
	if needsSealing {
		a.saveToFile()
		log.Println("Rewrote the connection file with encrypted passwords")
	}
	log.Printf("Loaded %d connections from %s", len(conns), a.filePath)
}

func (a *App) saveToFile() {
	if a.filePath == "" {
		return // no state directory configured (tests build bare Apps)
	}
	sealed := make([]ConnectionConfig, len(a.savedConnections))
	copy(sealed, a.savedConnections)
	for i := range sealed {
		password, err := protectSecret(sealed[i].Password)
		if err != nil {
			log.Printf("Failed to encrypt password for %s: %v", sealed[i].ID, err)
			return
		}
		sealed[i].Password = password
	}

	// The Fyne client shares this file. Fold in the connections it saved
	// since we last saw the file, so one client adding a connection no longer
	// erases the other's. Entries this process already observed are owned by
	// memory: their absence here is this save's deletion, not someone else's
	// write, so they are not resurrected.
	observed := make(map[string]bool, len(a.connObserved))
	for _, c := range a.connObserved {
		observed[connKey(c)] = true
	}
	if disk, ok, err := readJSONFile[ConnectionConfig](a.filePath); err == nil && ok {
		known := make(map[string]bool, len(sealed))
		for _, c := range sealed {
			known[connKey(c)] = true
		}
		for _, c := range disk {
			key := connKey(c)
			if known[key] || observed[key] {
				continue
			}
			known[key] = true
			sealed = append(sealed, c)
			// Adopt the connection into memory too, so the next save treats
			// it as this process's own instead of dropping it again. As at
			// load time, a password that cannot be opened here (another
			// Windows account sealed it) is left empty for the user to retype.
			if pw, err := unprotectSecret(c.Password); err == nil {
				c.Password = pw
			} else {
				log.Printf("Connection %s: %v", c.ID, err)
				c.Password = ""
			}
			a.savedConnections = append(a.savedConnections, c)
		}
	} else if err != nil {
		log.Printf("Failed to load connections for merging: %v", err)
	}

	data, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		log.Printf("Failed to save connections: %v", err)
		return
	}
	if err := writeFileAtomic(a.filePath, data); err != nil {
		log.Printf("Failed to write connections file: %v", err)
		return
	}
	a.connObserved = append([]ConnectionConfig(nil), sealed...)
}

func (a *App) Init(ctx context.Context) {
	a.ctx = ctx
	log.Println("TablePro Go started")
}

func (a *App) Shutdown(ctx context.Context) {
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()
	for id, conn := range a.connections {
		if conn.DB != nil {
			conn.DB.Close()
		}
		conn.closePools()
		delete(a.connections, id)
	}
	log.Println("TablePro Go shutdown")
}

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
	DB       *sql.DB           `json:"-"`
	Config   ConnectionConfig  `json:"-"`

	poolsMu   sync.Mutex
	pools     map[string]*sql.DB
	poolOrder []string // per-database pool LRU, least-recently-used first
}

// poolFor returns the *sql.DB to introspect the given database with. PostgreSQL
// cannot see other databases over an existing connection, so those get a
// lazily opened, cached pool of their own; the other engines answer metadata
// queries through qualified names on the main pool, which keeps browsing the
// tree from opening one connection per database.
func (a *App) poolFor(conn *Connection, database string) (*sql.DB, error) {
	if conn.DB == nil {
		return nil, fmt.Errorf("not connected")
	}
	if conn.Type == "postgresql" && database != "" && database != conn.Database {
		return a.perDatabasePool(conn, database)
	}
	return conn.DB, nil
}

// queryPool returns the pool a statement runs on. Unlike metadata lookups a
// statement resolves unqualified table names against the session's own
// database, so a tab that targets another one needs that database opened as a
// session of its own.
func (a *App) queryPool(conn *Connection, database string) (*sql.DB, error) {
	if database != "" && database != conn.Database {
		switch conn.Type {
		case "mysql", "sqlserver":
			return a.perDatabasePool(conn, database)
		}
	}
	return a.poolFor(conn, database)
}

func (a *App) perDatabasePool(conn *Connection, database string) (*sql.DB, error) {
	conn.poolsMu.Lock()
	defer conn.poolsMu.Unlock()
	if db, ok := conn.pools[database]; ok {
		conn.touchPool(database)
		return db, nil
	}

	config := conn.Config
	config.Database = database
	db, err := createConnection(config)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %q: %w", database, err)
	}
	if conn.pools == nil {
		conn.pools = make(map[string]*sql.DB)
	}
	// Browsing a server with hundreds of databases must not accumulate one
	// live pool per database; the oldest-unused pool is closed instead.
	// Desktop requests are short and serialized per pool, so the evicted
	// pool is not in use in practice.
	for len(conn.pools) >= maxPoolsPerConnection {
		conn.evictOldestPool()
	}
	conn.pools[database] = db
	conn.poolOrder = append(conn.poolOrder, database)
	return db, nil
}

// touchPool moves database to the most-recently-used end of the LRU order.
// Callers hold poolsMu.
func (c *Connection) touchPool(database string) {
	for i, name := range c.poolOrder {
		if name == database {
			c.poolOrder = append(c.poolOrder[:i], c.poolOrder[i+1:]...)
			break
		}
	}
	c.poolOrder = append(c.poolOrder, database)
}

// evictOldestPool closes the least-recently-used cached pool. Callers hold
// poolsMu.
func (c *Connection) evictOldestPool() {
	if len(c.poolOrder) == 0 {
		return
	}
	oldest := c.poolOrder[0]
	c.poolOrder = c.poolOrder[1:]
	if db, ok := c.pools[oldest]; ok {
		db.Close()
		delete(c.pools, oldest)
	}
}

func (c *Connection) closePools() {
	c.poolsMu.Lock()
	defer c.poolsMu.Unlock()
	for name, db := range c.pools {
		db.Close()
		delete(c.pools, name)
	}
	c.poolOrder = nil
}

type ConnectionConfig struct {
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

type QueryResult struct {
	Columns       []Column        `json:"columns"`
	Rows          [][]interface{} `json:"rows"`
	RowCount      int             `json:"rowCount"`
	AffectedRows  int64           `json:"affectedRows"`
	LastInsertID  int64           `json:"lastInsertId"`
	ExecutionTime float64         `json:"executionTime"`
	Error         string          `json:"error,omitempty"`
	// Truncated says the row cap cut the answer short, so a grid full of
	// exactly "limit" rows is not mistaken for the whole story.
	Truncated bool `json:"truncated,omitempty"`
}

type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

type TableInfo struct {
	Name    string `json:"name"`
	Schema  string `json:"schema"`
	Type    string `json:"type"`
	Comment string `json:"comment"`
}

type ColumnInfo struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	DefaultValue string `json:"defaultValue,omitempty"`
	IsPrimaryKey bool   `json:"isPrimaryKey"`
	Comment      string `json:"comment"`
}

func (a *App) TestConnection(config ConnectionConfig) error {
	log.Printf("Testing connection: type=%s host=%s:%d db=%s user=%s encrypt=%s",
		config.Type, config.Host, config.Port, config.Database, config.Username, config.SSLMode)
	db, err := createConnection(config)
	if err != nil {
		log.Printf("createConnection error: %v", err)
		return fmt.Errorf("connection failed: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
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
		clean.ID = fmt.Sprintf("conn_%d", i)
		clean.Password = ""
		result[i] = clean
	}
	return result
}

func (a *App) AddConnection(config ConnectionConfig) error {
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()

	config.ID = fmt.Sprintf("conn_%d", len(a.savedConnections))
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
			a.savedConnections[i] = config
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
			connID := fmt.Sprintf("conn_%d", i)
			if conn, ok := a.connections[connID]; ok && conn.DB != nil {
				conn.DB.Close()
				delete(a.connections, connID)
			}
			a.savedConnections = append(a.savedConnections[:i], a.savedConnections[i+1:]...)
			a.saveToFile()
			return nil
		}
	}
	return fmt.Errorf("connection not found: %s", id)
}

func (a *App) Connect(id string) error {
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()

	var index int
	n, err := fmt.Sscanf(id, "conn_%d", &index)
	if err != nil || n != 1 || index < 0 || index >= len(a.savedConnections) {
		return fmt.Errorf("invalid connection id: %s", id)
	}

	config := a.savedConnections[index]

	if existing, ok := a.connections[id]; ok {
		if existing.DB != nil {
			existing.DB.Close()
		}
		existing.closePools()
	}

	db, err := createConnection(config)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}

	a.connections[id] = &Connection{
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
		DB:       db,
		Config:   config,
	}
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
		conn.closePools()
	}
	if a.currentConnID == connID {
		a.currentConnID = ""
	}
	return nil
}

func (a *App) GetDatabases(connID string) ([]string, error) {
	a.connectionsMu.RLock()
	conn, ok := a.connections[connID]
	a.connectionsMu.RUnlock()

	if !ok || conn.DB == nil {
		return nil, fmt.Errorf("not connected")
	}

	var query string
	switch conn.Type {
	case "mysql":
		query = "SHOW DATABASES"
	case "postgresql":
		query = "SELECT datname FROM pg_database WHERE datistemplate = false"
	case "sqlite":
		return []string{conn.Database}, nil
	case "sqlserver":
		query = "SELECT name FROM sys.databases ORDER BY name"
	case "oracle":
		query = "SELECT name FROM v$database ORDER BY name"
	default:
		return nil, fmt.Errorf("unsupported type: %s", conn.Type)
	}

	rows, err := conn.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var databases []string
	filter := a.savedDatabaseFilter(connID)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if filter.allows(name) {
			databases = append(databases, name)
		}
	}
	return databases, nil
}

// savedDatabaseFilter reads the filter from the persisted config so a change
// applies to an already-open connection without reconnecting. IDs are
// index-based, matching Connect.
func (a *App) savedDatabaseFilter(connID string) DatabaseFilter {
	a.connectionsMu.RLock()
	defer a.connectionsMu.RUnlock()
	for i, saved := range a.savedConnections {
		if fmt.Sprintf("conn_%d", i) == connID {
			return saved.DatabaseFilter
		}
	}
	return DatabaseFilter{}
}

func (a *App) GetSchemas(connID string, database string) ([]string, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}

	// One collection loop serves both schema engines; only the catalog query
	// differs. PostgreSQL answers through its per-database pool, SQL Server
	// through a qualified name on the main pool.
	var db *sql.DB
	var query string
	switch conn.Type {
	case "sqlserver":
		db = conn.DB
		name, err := quoteIdent(conn.Type, database)
		if err != nil {
			return nil, err
		}
		query = fmt.Sprintf(`SELECT s.name FROM %s.sys.schemas s
			WHERE s.name NOT LIKE 'db[_]%%' AND s.name NOT IN ('sys', 'INFORMATION_SCHEMA', 'guest')
			ORDER BY s.name`, name)
	case "postgresql":
		var err error
		db, err = a.poolFor(conn, database)
		if err != nil {
			return nil, err
		}
		query = `SELECT nspname FROM pg_namespace
			WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
			ORDER BY nspname`
	default:
		// mysql uses the database as its schema, sqlite has a single main schema,
		// oracle is introspected through the connected user.
		return nil, nil
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		schemas = append(schemas, name)
	}
	return schemas, nil
}

func (a *App) GetTables(connID string, database string, schema string) ([]TableInfo, error) {
	return a.getTablesFiltered(connID, database, schema, "", false)
}

// getTablesFiltered is GetTables with an optional predicate on the object's
// name. A caller that needs to know whether the database holds one particular
// object asks directly instead of pulling the whole catalog: the payload stays
// one row no matter how many tables the schema has.
func (a *App) getTablesFiltered(connID string, database string, schema string, exact string, insensitive bool) ([]TableInfo, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}

	schema = defaultSchema(conn.Type, schema)

	db := conn.DB
	if conn.Type == "postgresql" {
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
	}
	query, args, err := tablesQuery(conn.Type, database, schema, exact, insensitive)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Name, &t.Type, &t.Comment); err != nil {
			return nil, err
		}
		t.Type = normalizeTableType(t.Type)
		t.Schema = schema
		tables = append(tables, t)
	}
	return tables, nil
}

// tablesQuery is the catalog statement behind GetTables, one dialect switch
// kept honest by tests. With an exact name it narrows the listing to that
// object; insensitive lowercases both sides, which is how the rename
// collision check has always compared names, while an action's target has to
// match the catalog's own spelling exactly.
func tablesQuery(dbType, database, schema, exact string, insensitive bool) (string, []interface{}, error) {
	switch dbType {
	case "mysql":
		query := `SELECT TABLE_NAME, TABLE_TYPE, TABLE_COMMENT FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ?`
		args := []interface{}{database}
		if exact != "" {
			if insensitive {
				query += ` AND LOWER(TABLE_NAME) = LOWER(?)`
			} else {
				query += ` AND TABLE_NAME = ?`
			}
			args = append(args, exact)
		}
		return query + ` ORDER BY TABLE_NAME`, args, nil
	case "postgresql":
		tables, views := "", ""
		args := []interface{}{schema}
		if exact != "" {
			if insensitive {
				tables = ` AND LOWER(tablename) = LOWER($2)`
				views = ` AND LOWER(viewname) = LOWER($3)`
			} else {
				tables = ` AND tablename = $2`
				views = ` AND viewname = $3`
			}
			args = append(args, exact, exact)
		}
		query := fmt.Sprintf(`SELECT tablename, 'BASE TABLE', '' FROM pg_tables WHERE schemaname = $1%s
			UNION ALL
			SELECT viewname, 'VIEW', '' FROM pg_views WHERE schemaname = $1%s
			ORDER BY 1`, tables, views)
		return query, args, nil
	case "sqlite":
		query := `SELECT name, type, '' FROM sqlite_master
			WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'`
		var args []interface{}
		if exact != "" {
			if insensitive {
				query += ` AND LOWER(name) = LOWER(?)`
			} else {
				query += ` AND name = ?`
			}
			args = append(args, exact)
		}
		return query + ` ORDER BY name`, args, nil
	case "sqlserver":
		name, err := quoteIdent(dbType, database)
		if err != nil {
			return "", nil, err
		}
		// go-mssqldb leaves "?" markers untouched in some statements, so the
		// names are inlined as escaped literals instead.
		return fmt.Sprintf(`SELECT TABLE_NAME, TABLE_TYPE, '' FROM %s.INFORMATION_SCHEMA.TABLES
			WHERE TABLE_SCHEMA = %s%s ORDER BY TABLE_NAME`, name,
			quotedLiteral(dbType, schema), namePredicate(dbType, "TABLE_NAME", exact, insensitive)), nil, nil
	case "oracle":
		// go-ora wants ":name" markers and the catalog lookups elsewhere in
		// this file inline escaped literals instead, which it never rewrites.
		return `SELECT OBJECT_NAME, OBJECT_TYPE, '' FROM ALL_OBJECTS
			WHERE OBJECT_TYPE IN ('TABLE', 'VIEW') AND OWNER = USER` +
			namePredicate(dbType, "OBJECT_NAME", exact, insensitive) + ` ORDER BY OBJECT_NAME`, nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported type: %s", dbType)
	}
}

// namePredicate is the inlined-literal form of the exact-name filter, for the
// engines whose drivers make binds unreliable.
func namePredicate(dbType, column, exact string, insensitive bool) string {
	if exact == "" {
		return ""
	}
	literal := quotedLiteral(dbType, exact)
	if insensitive {
		return fmt.Sprintf(` AND LOWER(%s) = LOWER(%s)`, column, literal)
	}
	return fmt.Sprintf(` AND %s = %s`, column, literal)
}

// lookupObject answers "is this name in this schema, and what is it" without
// dragging the whole catalog back with it.
func (a *App) lookupObject(connID, database, schema, name string, insensitive bool) (*TableInfo, error) {
	if name == "" {
		return nil, nil
	}
	matches, err := a.getTablesFiltered(connID, database, schema, name, insensitive)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &matches[0], nil
}

func (a *App) GetColumns(connID string, database string, schema string, table string) ([]ColumnInfo, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}

	if schema == "" {
		schema = resolveSchema(conn, database, schema)
	}

	db := conn.DB
	var query string
	var args []interface{}
	switch conn.Type {
	case "mysql":
		query = `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT,
			CASE WHEN COLUMN_KEY = 'PRI' THEN 1 ELSE 0 END, COLUMN_COMMENT
			FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
			ORDER BY ORDINAL_POSITION`
		args = []interface{}{schema, table}
	case "postgresql":
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
		query = `SELECT a.attname,
			format_type(a.atttypid, a.atttypmod),
			CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END,
			COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
			CASE WHEN i.indisprimary THEN 1 ELSE 0 END,
			COALESCE(col_description(c.oid, a.attnum), '')
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
			LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
			LEFT JOIN pg_index i ON i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY(i.indkey)
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'v', 'm', 'p')
			ORDER BY a.attnum`
		args = []interface{}{schema, table}
	case "sqlite":
		name, err := quoteIdent(conn.Type, table)
		if err != nil {
			return nil, err
		}
		query = fmt.Sprintf("PRAGMA table_info(%s)", name)
	case "sqlserver":
		name, err := quoteIdent(conn.Type, database)
		if err != nil {
			return nil, err
		}
		// MSSQL keeps column comments in extended properties. The lookup goes
		// through sys.columns because COLUMNPROPERTY cannot resolve a
		// database-qualified name, and this query may run against another database.
		dbLiteral := quotedLiteral("sqlserver", name)
		objectID := fmt.Sprintf("OBJECT_ID(%s + '.' + QUOTENAME(c.TABLE_SCHEMA) + '.' + QUOTENAME(c.TABLE_NAME))", dbLiteral)
		comment := fmt.Sprintf(`ISNULL((SELECT TOP 1 CAST(ep.value AS NVARCHAR(4000))
			FROM %s.sys.extended_properties ep
			INNER JOIN %s.sys.columns sc
				ON sc.object_id = ep.major_id AND sc.column_id = ep.minor_id
			WHERE ep.class = 1 AND ep.name = 'MS_Description'
				AND sc.object_id = %s AND sc.name = c.COLUMN_NAME), '')`, name, name, objectID)
		query = fmt.Sprintf(`SELECT c.COLUMN_NAME, c.DATA_TYPE, c.IS_NULLABLE, c.COLUMN_DEFAULT,
			CASE WHEN pk.COLUMN_NAME IS NOT NULL THEN 1 ELSE 0 END, %s
			FROM %s.INFORMATION_SCHEMA.COLUMNS c
			LEFT JOIN (
				SELECT ku.TABLE_SCHEMA, ku.TABLE_NAME, ku.COLUMN_NAME
				FROM %s.INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
				INNER JOIN %s.INFORMATION_SCHEMA.KEY_COLUMN_USAGE ku
					ON ku.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
					AND ku.TABLE_NAME = tc.TABLE_NAME AND ku.TABLE_SCHEMA = tc.TABLE_SCHEMA
				WHERE tc.CONSTRAINT_TYPE = 'PRIMARY KEY'
			) pk ON pk.TABLE_SCHEMA = c.TABLE_SCHEMA AND pk.TABLE_NAME = c.TABLE_NAME
				AND pk.COLUMN_NAME = c.COLUMN_NAME
			WHERE c.TABLE_NAME = %s AND c.TABLE_SCHEMA = %s
			ORDER BY c.ORDINAL_POSITION`, comment, name, name, name, quotedLiteral("sqlserver", table), quotedLiteral("sqlserver", schema))
	case "oracle":
		query = `SELECT c.COLUMN_NAME, c.DATA_TYPE, c.NULLABLE, c.DATA_DEFAULT,
			CASE WHEN cc.COLUMN_NAME IS NOT NULL THEN 1 ELSE 0 END, c.COMMENTS
			FROM ALL_TAB_COLUMNS c
			LEFT JOIN (
				SELECT cols.COLUMN_NAME
				FROM ALL_CONSTRAINTS cons
				INNER JOIN ALL_CONS_COLUMNS cols
					ON cons.CONSTRAINT_NAME = cols.CONSTRAINT_NAME
				WHERE cons.TABLE_NAME = '` + strings.ReplaceAll(table, "'", "''") + `' AND cons.CONSTRAINT_TYPE = 'P'
			) cc ON c.COLUMN_NAME = cc.COLUMN_NAME
			WHERE c.TABLE_NAME = '` + strings.ReplaceAll(table, "'", "''") + `' AND c.OWNER = USER
			ORDER BY c.COLUMN_ID`
	default:
		return nil, fmt.Errorf("unsupported type: %s", conn.Type)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var defaultValue sql.NullString
		var isPrimaryKey int

		if conn.Type == "sqlite" {
			// table_info's fourth field is *notnull*, the opposite of the
			// YES/NO the other engines answer, so it gets its own reading.
			var cid, notnull, pk int
			if err := rows.Scan(&cid, &col.Name, &col.Type, &notnull, &defaultValue, &pk); err != nil {
				return nil, err
			}
			isPrimaryKey = pk
			col.Nullable = notnull == 0
		} else {
			var nullable string
			if err := rows.Scan(&col.Name, &col.Type, &nullable, &defaultValue, &isPrimaryKey, &col.Comment); err != nil {
				return nil, err
			}
			col.Nullable = nullable == "YES" || nullable == "1" || nullable == "Y"
		}

		col.IsPrimaryKey = isPrimaryKey == 1
		if defaultValue.Valid {
			col.DefaultValue = defaultValue.String
		}
		columns = append(columns, col)
	}
	return columns, nil
}

// IndexInfo describes an index or a unique/primary key constraint on a table.
type IndexInfo struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
	Columns []string `json:"columns"`
}

// ForeignKeyInfo describes one constraint pointing at another table; composite
// keys keep their column pairs in catalog order.
type ForeignKeyInfo struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete"`
	OnUpdate   string   `json:"onUpdate"`
}

// TableStructure backs the object tab's structure page. CreateTable is rendered
// from the same rows the UI shows, so what the user reads is what they copy.
type TableStructure struct {
	Columns     []ColumnInfo     `json:"columns"`
	Indexes     []IndexInfo      `json:"indexes"`
	ForeignKeys []ForeignKeyInfo `json:"foreignKeys"`
	CreateTable string           `json:"createTable"`
}

func resolveSchema(conn *Connection, database, schema string) string {
	switch conn.Type {
	case "sqlserver":
		return "dbo"
	case "postgresql":
		return "public"
	case "mysql":
		return database
	}
	return schema
}

// indexRow is the flat, one-row-per-column shape the index catalogs return;
// the FK catalogs answer in the shared tableprosql.FKRow, and the aggregated
// lists are what the UI and the DDL want.
type indexRow struct {
	name    string
	kind    string
	column  string
	unique  bool
	primary bool
}

func aggregateIndexes(rows []indexRow) []IndexInfo {
	indexes := []IndexInfo{}
	byName := map[string]int{}
	for _, row := range rows {
		i, ok := byName[row.name]
		if !ok {
			byName[row.name] = len(indexes)
			indexes = append(indexes, IndexInfo{
				Name:    row.name,
				Kind:    row.kind,
				Unique:  row.unique,
				Primary: row.primary,
			})
			i = len(indexes) - 1
		}
		indexes[i].Columns = append(indexes[i].Columns, row.column)
	}
	return indexes
}

func aggregateForeignKeys(rows []tableprosql.FKRow) []ForeignKeyInfo {
	foreignKeys := []ForeignKeyInfo{}
	byName := map[string]int{}
	for _, row := range rows {
		i, ok := byName[row.Name]
		if !ok {
			byName[row.Name] = len(foreignKeys)
			foreignKeys = append(foreignKeys, ForeignKeyInfo{
				Name:      row.Name,
				RefSchema: row.RefSchema,
				RefTable:  row.RefTable,
				OnDelete:  row.OnDelete,
				OnUpdate:  row.OnUpdate,
			})
			i = len(foreignKeys) - 1
		}
		foreignKeys[i].Columns = append(foreignKeys[i].Columns, row.Column)
		foreignKeys[i].RefColumns = append(foreignKeys[i].RefColumns, row.RefColumn)
	}
	for i := range foreignKeys {
		foreignKeys[i].OnDelete = tableprosql.ReferenceAction(foreignKeys[i].OnDelete)
		foreignKeys[i].OnUpdate = tableprosql.ReferenceAction(foreignKeys[i].OnUpdate)
	}
	return foreignKeys
}

func (a *App) tableIndexes(conn *Connection, database, schema, table string) ([]IndexInfo, error) {
	db := conn.DB
	var query string
	var args []interface{}

	switch conn.Type {
	case "mysql":
		query = `SELECT INDEX_NAME, INDEX_TYPE, NON_UNIQUE, COLUMN_NAME
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
			ORDER BY INDEX_NAME, SEQ_IN_INDEX`
		args = []interface{}{schema, table}
	case "postgresql":
		var err error
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
		query = `SELECT c.relname, am.amname, CASE WHEN i.indisunique THEN 1 ELSE 0 END,
			CASE WHEN i.indisprimary THEN 1 ELSE 0 END, x.attname
			FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_am am ON am.oid = c.relam
			JOIN pg_class t ON t.oid = i.indrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
			JOIN pg_attribute x ON x.attrelid = i.indrelid AND x.attnum = k.attnum
			WHERE n.nspname = $1 AND t.relname = $2
			ORDER BY c.relname, k.ord`
		args = []interface{}{schema, table}
	case "sqlserver":
		// Indexes are per-object, so the catalog of the target database is
		// reached through a qualified name instead of a separate connection.
		quoted, err := quoteIdent(conn.Type, database)
		if err != nil {
			return nil, err
		}
		objectID, err := qualifiedName(conn.Type, database, schema, table)
		if err != nil {
			return nil, err
		}
		literal := quotedLiteral("sqlserver", objectID)
		query = fmt.Sprintf(`SELECT i.name, i.type_desc, CAST(i.is_unique AS int),
			CAST(i.is_primary_key AS int), col.name
			FROM %s.sys.indexes i
			INNER JOIN %s.sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
			INNER JOIN %s.sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
			WHERE i.object_id = OBJECT_ID(%s) AND i.is_hypothetical = 0
			ORDER BY i.name, ic.key_ordinal`, quoted, quoted, quoted, literal)
	case "oracle":
		query = fmt.Sprintf(`SELECT i.index_name, i.index_type,
			CASE WHEN i.uniqueness = 'UNIQUE' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS (SELECT 1 FROM ALL_CONSTRAINTS ac
				WHERE ac.constraint_type = 'P' AND ac.owner = i.owner AND ac.index_name = i.index_name)
			THEN 1 ELSE 0 END,
			c.column_name
			FROM ALL_INDEXES i
			JOIN ALL_IND_COLUMNS c ON c.index_owner = i.owner AND c.index_name = i.index_name
				AND c.table_owner = i.owner AND c.table_name = i.table_name
			WHERE i.table_name = %s AND i.owner = USER
			ORDER BY i.index_name, c.column_position`, oraLiteral(table))
	case "sqlite":
		return a.sqliteIndexes(db, table)
	default:
		return nil, fmt.Errorf("unsupported type: %s", conn.Type)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flat []indexRow
	for rows.Next() {
		var row indexRow
		var unique, primary int
		if err := rows.Scan(&row.name, &row.kind, &unique, &primary, &row.column); err != nil {
			return nil, err
		}
		row.unique, row.primary = unique == 1, primary == 1
		if conn.Type == "mysql" && row.name == "PRIMARY" {
			row.primary = true
		}
		flat = append(flat, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aggregateIndexes(flat), nil
}

// sqliteIndexes walks index_list/index_info because pragmas cannot be joined.
func (a *App) sqliteIndexes(db *sql.DB, table string) ([]IndexInfo, error) {
	name, err := quoteIdent("sqlite", table)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(fmt.Sprintf("PRAGMA index_list(%s)", name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type entry struct {
		name   string
		origin string
		unique bool
	}
	var entries []entry
	for rows.Next() {
		var seq int
		var e entry
		var unique int
		var partial string
		if err := rows.Scan(&seq, &e.name, &unique, &e.origin, &partial); err != nil {
			return nil, err
		}
		e.unique = unique == 1
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	indexes := []IndexInfo{}
	for _, e := range entries {
		// Implicit indexes SQLite builds for UNIQUE/PRIMARY KEY constraints are
		// already covered by the constraint itself.
		if strings.HasPrefix(e.name, "sqlite_autoindex") {
			continue
		}
		columns, err := sqliteIndexColumns(db, e.name)
		if err != nil {
			return nil, err
		}
		indexes = append(indexes, IndexInfo{
			Name:    e.name,
			Kind:    e.origin,
			Unique:  e.unique,
			Primary: e.origin == "pk",
			Columns: columns,
		})
	}
	return indexes, nil
}

func sqliteIndexColumns(db *sql.DB, index string) ([]string, error) {
	name, err := quoteIdent("sqlite", index)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA index_info(%s)", name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := []string{}
	for rows.Next() {
		var seqno, cid int
		var column string
		if err := rows.Scan(&seqno, &cid, &column); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func (a *App) tableForeignKeys(conn *Connection, database, schema, table string) ([]ForeignKeyInfo, error) {
	db := conn.DB
	var query string
	var args []interface{}

	switch conn.Type {
	case "mysql":
		query = `SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA,
			k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
			IFNULL(r.DELETE_RULE, ''), IFNULL(r.UPDATE_RULE, '')
			FROM information_schema.KEY_COLUMN_USAGE k
			LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS r
				ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
			WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
			ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`
		args = []interface{}{schema, table}
	case "postgresql":
		var err error
		if db, err = a.poolFor(conn, database); err != nil {
			return nil, err
		}
		query = `SELECT con.conname, xg.attname, confnsp.nspname, confcls.relname, yg.attname,
			COALESCE(CASE con.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
				WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END, ''),
			COALESCE(CASE con.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
				WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END, '')
			FROM pg_constraint con
			JOIN pg_class cls ON cls.oid = con.conrelid
			JOIN pg_namespace nsp ON nsp.oid = cls.relnamespace
			JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS x(attnum, ord) ON true
			JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS y(attnum, ord) ON y.ord = x.ord
			JOIN pg_attribute xg ON xg.attrelid = con.conrelid AND xg.attnum = x.attnum
			JOIN pg_attribute yg ON yg.attrelid = con.confrelid AND yg.attnum = y.attnum
			JOIN pg_class confcls ON confcls.oid = con.confrelid
			JOIN pg_namespace confnsp ON confnsp.oid = confcls.relnamespace
			WHERE con.contype = 'f' AND nsp.nspname = $1 AND cls.relname = $2
			ORDER BY con.conname, x.ord`
		args = []interface{}{schema, table}
	case "sqlserver":
		quoted, err := quoteIdent(conn.Type, database)
		if err != nil {
			return nil, err
		}
		objectID, err := qualifiedName(conn.Type, database, schema, table)
		if err != nil {
			return nil, err
		}
		literal := quotedLiteral("sqlserver", objectID)
		// Every name comes from a joined catalog row rather than
		// OBJECT_SCHEMA_NAME/COL_NAME, because those resolve against the
		// session's own database and this pool may sit in another one.
		query = fmt.Sprintf(`SELECT fk.name, col.name, rs.name, ref.name, refcol.name,
			fk.delete_referential_action_desc, fk.update_referential_action_desc
			FROM %s.sys.foreign_keys fk
			INNER JOIN %s.sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
			INNER JOIN %s.sys.columns col ON col.object_id = fkc.parent_object_id
				AND col.column_id = fkc.parent_column_id
			INNER JOIN %s.sys.tables ref ON ref.object_id = fk.referenced_object_id
			INNER JOIN %s.sys.schemas rs ON rs.schema_id = ref.schema_id
			INNER JOIN %s.sys.columns refcol ON refcol.object_id = fkc.referenced_object_id
				AND refcol.column_id = fkc.referenced_column_id
			WHERE fk.parent_object_id = OBJECT_ID(%s)
			ORDER BY fk.name, fkc.constraint_column_id`,
			quoted, quoted, quoted, quoted, quoted, quoted, literal)
	case "oracle":
		query = fmt.Sprintf(`SELECT ac.constraint_name, cc.column_name, rac.owner, rac.table_name,
			rcc.column_name, ac.delete_rule, ''
			FROM ALL_CONSTRAINTS ac
			JOIN ALL_CONS_COLUMNS cc ON cc.owner = ac.owner AND cc.constraint_name = ac.constraint_name
			JOIN ALL_CONSTRAINTS rac ON rac.owner = ac.r_owner AND rac.constraint_name = ac.r_constraint_name
			JOIN ALL_CONS_COLUMNS rcc ON rcc.owner = rac.owner AND rcc.constraint_name = rac.constraint_name
				AND rcc.position = cc.position
			WHERE ac.constraint_type = 'R' AND ac.owner = USER AND ac.table_name = %s
			ORDER BY ac.constraint_name, cc.position`, oraLiteral(table))
	case "sqlite":
		return a.sqliteForeignKeys(db, table)
	default:
		return nil, fmt.Errorf("unsupported type: %s", conn.Type)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flat []tableprosql.FKRow
	for rows.Next() {
		var row tableprosql.FKRow
		if err := rows.Scan(&row.Name, &row.Column, &row.RefSchema, &row.RefTable, &row.RefColumn,
			&row.OnDelete, &row.OnUpdate); err != nil {
			return nil, err
		}
		flat = append(flat, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aggregateForeignKeys(flat), nil
}

func (a *App) sqliteForeignKeys(db *sql.DB, table string) ([]ForeignKeyInfo, error) {
	name, err := quoteIdent("sqlite", table)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA foreign_key_list(%s)", name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flat []tableprosql.FKRow
	for rows.Next() {
		var id, seq int
		var row tableprosql.FKRow
		var onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &row.RefTable, &row.Column, &row.RefColumn,
			&onUpdate, &onDelete, &match); err != nil {
			return nil, err
		}
		row.Name = fmt.Sprintf("fk_%d", id)
		row.OnUpdate, row.OnDelete = onUpdate, onDelete
		flat = append(flat, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return aggregateForeignKeys(flat), nil
}

func oraLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (a *App) GetTableStructure(connID, database, schema, table string) (TableStructure, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return TableStructure{}, err
	}
	schema = resolveSchema(conn, database, schema)

	columns, err := a.GetColumns(connID, database, schema, table)
	if err != nil {
		return TableStructure{}, err
	}
	indexes, err := a.tableIndexes(conn, database, schema, table)
	if err != nil {
		return TableStructure{}, err
	}
	foreignKeys, err := a.tableForeignKeys(conn, database, schema, table)
	if err != nil {
		return TableStructure{}, err
	}

	types := fullColumnTypes(conn, database, schema, table)
	// MSSQL and Oracle report bare type names through GetColumns, so the column
	// list would otherwise read "varchar" where the DDL says "varchar(50)".
	for i := range columns {
		if full, ok := types[columns[i].Name]; ok && full != "" {
			columns[i].Type = full
		}
	}
	ddl := buildCreateTable(conn.Type, schema, table, columns, types, indexes, foreignKeys)
	// SQL Server wraps catalog defaults in parentheses; the readable form
	// belongs in the column list, the stored form in the generated DDL.
	for i := range columns {
		columns[i].DefaultValue = normalizeDefault(conn.Type, columns[i].DefaultValue)
	}

	return TableStructure{
		Columns:     columns,
		Indexes:     indexes,
		ForeignKeys: foreignKeys,
		CreateTable: ddl,
	}, nil
}

// fullColumnTypes returns name -> type with lengths and precision. MySQL,
// PostgreSQL and SQLite already report the full type through GetColumns;
// MSSQL and Oracle report bare type names, which would build invalid DDL.
func fullColumnTypes(conn *Connection, database, schema, table string) map[string]string {
	db := conn.DB
	var query string
	var args []interface{}

	switch conn.Type {
	case "sqlserver":
		quoted, err := quoteIdent(conn.Type, database)
		if err != nil {
			return nil
		}
		query = fmt.Sprintf(`SELECT COLUMN_NAME,
			DATA_TYPE + CASE
				WHEN DATA_TYPE IN ('varchar', 'char', 'varbinary', 'binary', 'nchar') THEN
					CASE WHEN CHARACTER_MAXIMUM_LENGTH = -1 THEN '(max)'
						ELSE '(' + CAST(CHARACTER_MAXIMUM_LENGTH AS varchar(20)) + ')' END
				WHEN DATA_TYPE = 'nvarchar' THEN
					CASE WHEN CHARACTER_MAXIMUM_LENGTH = -1 THEN '(max)'
						ELSE '(' + CAST(CHARACTER_MAXIMUM_LENGTH / 2 AS varchar(20)) + ')' END
				WHEN DATA_TYPE IN ('decimal', 'numeric') THEN
					'(' + CAST(NUMERIC_PRECISION AS varchar(20)) + ', ' + CAST(NUMERIC_SCALE AS varchar(20)) + ')'
				WHEN DATA_TYPE = 'float' AND NUMERIC_PRECISION IS NOT NULL AND NUMERIC_PRECISION < 53 THEN
					'(' + CAST(NUMERIC_PRECISION AS varchar(20)) + ')'
				ELSE '' END
			FROM %s.INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_NAME = %s AND TABLE_SCHEMA = %s`, quoted, quotedLiteral("sqlserver", table), quotedLiteral("sqlserver", schema))
	case "oracle":
		query = fmt.Sprintf(`SELECT COLUMN_NAME,
			DATA_TYPE || CASE
				WHEN DATA_TYPE IN ('VARCHAR2', 'NVARCHAR2', 'CHAR', 'NCHAR') THEN '(' || DATA_LENGTH || ')'
				WHEN DATA_TYPE = 'NUMBER' AND DATA_PRECISION IS NOT NULL THEN
					'(' || DATA_PRECISION || CASE WHEN DATA_SCALE > 0 THEN ', ' || DATA_SCALE ELSE '' END || ')'
				ELSE '' END
			FROM ALL_TAB_COLUMNS
			WHERE TABLE_NAME = %s AND OWNER = USER`, oraLiteral(table))
	default:
		return nil
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	types := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			return nil
		}
		types[name] = strings.TrimSpace(def)
	}
	return types
}

func ddlIdent(dbType, part string) string {
	quoted, err := quoteIdent(dbType, part)
	if err != nil {
		return part
	}
	return quoted
}

func ddlColumns(dbType string, names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, ddlIdent(dbType, name))
	}
	return strings.Join(quoted, ", ")
}

// ddlTableName qualifies only when a schema exists, because SQLite has no
// schema argument in the tree and "main" would be noise in the DDL.
func ddlTableName(dbType, schema, table string) string {
	if schema == "" || schema == "main" {
		return ddlIdent(dbType, table)
	}
	return ddlIdent(dbType, schema) + "." + ddlIdent(dbType, table)
}

// buildCreateTable reconstructs the DDL from catalog rows. It is a reading aid
// rather than a migration tool, so engine-level storage clauses are left out.
func buildCreateTable(dbType, schema, table string, columns []ColumnInfo, types map[string]string, indexes []IndexInfo, foreignKeys []ForeignKeyInfo) string {
	object := ddlTableName(dbType, schema, table)

	definitions := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		dataType := column.Type
		if full, ok := types[column.Name]; ok && full != "" {
			dataType = full
		}
		line := "  " + ddlIdent(dbType, column.Name) + " " + dataType
		if value := strings.TrimSpace(column.DefaultValue); value != "" {
			line += " DEFAULT " + normalizeDefault(dbType, value)
		}
		if !column.Nullable {
			line += " NOT NULL"
		}
		definitions = append(definitions, line)
	}

	statements := []string{}
	for _, index := range indexes {
		if !index.Primary {
			continue
		}
		definitions = append(definitions,
			"  CONSTRAINT "+ddlIdent(dbType, index.Name)+" PRIMARY KEY ("+ddlColumns(dbType, index.Columns)+")")
	}
	statements = append(statements, "CREATE TABLE "+object+" (\n"+strings.Join(definitions, ",\n")+"\n);")

	for _, index := range indexes {
		if index.Primary || len(index.Columns) == 0 {
			continue
		}
		unique := ""
		if index.Unique {
			unique = "UNIQUE "
		}
		statements = append(statements, fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);",
			unique, ddlIdent(dbType, index.Name), object, ddlColumns(dbType, index.Columns)))
	}

	for _, fk := range foreignKeys {
		referenced := ddlTableName(dbType, fk.RefSchema, fk.RefTable)
		line := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)",
			object, ddlIdent(dbType, fk.Name), ddlColumns(dbType, fk.Columns), referenced, ddlColumns(dbType, fk.RefColumns))
		if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
			line += " ON DELETE " + fk.OnDelete
		}
		if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
			line += " ON UPDATE " + fk.OnUpdate
		}
		statements = append(statements, line+";")
	}

	return strings.Join(statements, "\n\n")
}

// normalizeDefault unwraps the parentheses MSSQL wraps around every default and
// trims the padding Oracle adds to expression text.
func normalizeDefault(dbType, value string) string {
	if dbType == "sqlserver" {
		for strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
			inner := strings.TrimSpace(value[1 : len(value)-1])
			if inner == "" {
				break
			}
			value = inner
		}
	}
	return value
}

// sqlCommentRe strips comments so a commented-out "FROM x" cannot be mistaken
// for the query's real table.
var sqlCommentRe = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)

// queryTableRe grabs the first object named in a FROM/JOIN/UPDATE/INTO clause.
var queryTableRe = regexp.MustCompile("(?is)\\b(?:from|join|update|into)\\s+([\\w.$@#\\[\\]\"`]+)")

// tableRef is a table name as written in a query, with each part unquoted.
type tableRef struct {
	database string
	schema   string
	table    string
}

func unquoteIdentPart(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}
	if len(part) >= 2 && part[0] == '[' && part[len(part)-1] == ']' {
		return strings.ReplaceAll(part[1:len(part)-1], "]]", "]")
	}
	quote := part[0]
	if len(part) >= 2 && (quote == '`' || quote == '"' || quote == '\'') && part[len(part)-1] == quote {
		return strings.ReplaceAll(part[1:len(part)-1], string([]byte{quote, quote}), string(quote))
	}
	return part
}

func parseTableRef(raw string) tableRef {
	parts := strings.Split(strings.Trim(raw, " \t\r\n"), ".")
	for i, part := range parts {
		parts[i] = unquoteIdentPart(part)
	}

	var ref tableRef
	switch len(parts) {
	case 1:
		ref.table = parts[0]
	case 2:
		ref.schema, ref.table = parts[0], parts[1]
	default:
		ref.database, ref.schema, ref.table = parts[len(parts)-3], parts[len(parts)-2], parts[len(parts)-1]
	}
	return ref
}

// QueryColumns resolves the table a query reads from and returns its columns,
// which is how the result grid finds column comments for "SELECT *" output.
// Names without a database prefix are looked up in database, then in the
// connection's own. Sub-selects, joins and unresolvable names return nothing.
func (a *App) QueryColumns(connID string, query string, database string) ([]ColumnInfo, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return nil, err
	}

	match := queryTableRe.FindStringSubmatch(sqlCommentRe.ReplaceAllString(query, " "))
	if match == nil {
		return nil, nil
	}
	ref := parseTableRef(match[1])
	if ref.table == "" {
		return nil, nil
	}

	switch {
	case ref.database != "":
		database = ref.database
	case database == "":
		database = conn.Database
	}
	return a.GetColumns(connID, database, ref.schema, ref.table)
}

// Preview reads the first rows of a table so the sidebar can show data without
// the user writing a query, narrowed by the filter bar when it holds any.
func (a *App) Preview(connID string, database string, schema string, table string, limit int, offset int, filters []TableFilter) QueryResult {
	conn, err := a.connection(connID)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	db := conn.DB
	schema = defaultSchema(conn.Type, schema)
	if conn.Type == "postgresql" {
		if db, err = a.poolFor(conn, database); err != nil {
			return QueryResult{Error: err.Error()}
		}
	}
	// The same renderer the update builder uses, so the grid and any edit
	// started from it address one and the same object.
	name, err := tableName(conn.Type, database, schema, table)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	// One catalog read serves the page: the key ORDER BY and the filter WHERE
	// both need the columns, and a page flip used to pay two round trips.
	columns, columnsErr := a.GetColumns(connID, database, schema, table)
	if columnsErr != nil && len(filters) > 0 {
		return QueryResult{Error: columnsErr.Error()}
	}
	orderBy := pageOrderFrom(conn.Type, columns, columnsErr)
	where, err := tableWhereFrom(conn, table, columns, filters)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	var query string
	switch conn.Type {
	case "mysql", "postgresql", "sqlite":
		query = fmt.Sprintf("SELECT * FROM %s%s%s LIMIT %d OFFSET %d", name, where, orderBy, limit, offset)
	case "sqlserver", "oracle":
		// Both need an ORDER BY to page. A constant one is what a keyless object
		// falls back to: the rows it splits across pages are then arbitrary, but
		// they are not shown twice or dropped.
		if orderBy == "" {
			if conn.Type == "sqlserver" {
				orderBy = " ORDER BY (SELECT NULL)"
			} else {
				orderBy = " ORDER BY 1"
			}
		}
		query = fmt.Sprintf("SELECT * FROM %s%s%s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", name, where, orderBy, offset, limit)
	default:
		return QueryResult{Error: fmt.Sprintf("unsupported type: %s", conn.Type)}
	}

	return runSelect(db, query, time.Now())
}

// pageOrderFrom sorts a browsed table by its key so consecutive pages cannot
// show one row twice or skip it. An object with no key - or one whose columns
// could not be read - gets no ordering at all rather than a guessed one.
func pageOrderFrom(dbType string, columns []ColumnInfo, columnsErr error) string {
	if columnsErr != nil {
		return ""
	}
	var keys []string
	for _, col := range columns {
		if !col.IsPrimaryKey {
			continue
		}
		name, err := quoteIdent(dbType, col.Name)
		if err != nil {
			return ""
		}
		keys = append(keys, name)
	}
	if len(keys) == 0 {
		return ""
	}
	return " ORDER BY " + strings.Join(keys, ", ")
}

func (a *App) connection(connID string) (*Connection, error) {
	a.connectionsMu.RLock()
	defer a.connectionsMu.RUnlock()

	conn, ok := a.connections[connID]
	if !ok || conn.DB == nil {
		return nil, fmt.Errorf("not connected")
	}
	return conn, nil
}

func normalizeTableType(raw string) string {
	if strings.Contains(strings.ToUpper(raw), "VIEW") {
		return "view"
	}
	return "table"
}

// quoteIdent safely quotes an identifier for each engine; the shapes live in
// the shared SQL text package both TablePro clients generate from.
func quoteIdent(dbType string, name string) (string, error) {
	return tableprosql.QuoteIdent(dbType, name)
}

func qualifiedName(dbType string, parts ...string) (string, error) {
	return tableprosql.QualifiedName(dbType, parts...)
}

// GetCreateTableTemplate renders the starter DDL for "新建表"; the dialects
// live in the shared SQL text package so the webview and the Fyne client
// open the editor on the same template.
func (a *App) GetCreateTableTemplate(dbType, schema string) string {
	return tableprosql.CreateTableTemplate(dbType, schema)
}

// ExecuteQuery runs a statement against database, which defaults to the
// database the connection opened with. An empty or matching name reuses the
// main pool, so only cross-database tabs pay for an extra connection.
func (a *App) ExecuteQuery(connID string, query string, database string) QueryResult {
	result := a.runQuery(connID, query, database)
	a.recordStatement(connID, database, query, result)
	return result
}

func (a *App) runQuery(connID string, query string, database string) QueryResult {
	conn, err := a.connection(connID)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	db, err := a.queryPool(conn, database)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	start := time.Now()
	if isSelectQuery(query) {
		limit := a.GetSettings().ResultLimit
		fetch := limit
		if limit > 0 {
			fetch = limit + 1
		}
		probe := withRowLimit(query, conn.Type, fetch)
		result := runSelect(db, probe, start)
		// The extra row is a probe only when the cap is ours: a statement with
		// a cap of its own runs untouched and is never marked truncated.
		if limit > 0 && probe != query && result.Error == "" && len(result.Rows) > limit {
			result.Rows = result.Rows[:limit]
			result.RowCount = limit
			result.Truncated = true
		}
		return result
	}
	return executeCommand(db, query, start)
}

func (a *App) loadHistory() {
	entries, ok, err := readJSONFile[HistoryEntry](a.historyPath)
	if err != nil {
		log.Printf("Failed to load history: %v", err)
		return
	}
	if !ok {
		return
	}
	a.history = entries
	a.histObserved = append([]HistoryEntry(nil), entries...)
	for _, e := range entries {
		if e.ID > a.historySeq {
			a.historySeq = e.ID
		}
	}
}

// statementTable extracts the object a statement touches, the same way the
// comment lookup does, so history can be filtered per table.
func statementTable(query string) string {
	match := queryTableRe.FindStringSubmatch(sqlCommentRe.ReplaceAllString(query, " "))
	if match == nil {
		return ""
	}
	ref := parseTableRef(match[1])
	if ref.table == "" || !isIdentifierLike(ref.table) {
		return ""
	}
	return strings.ToLower(ref.table)
}

func isIdentifierLike(name string) bool {
	for i, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return name != ""
}

func (a *App) recordStatement(connID, database, query string, result QueryResult) {
	sql := strings.TrimSpace(query)
	if sql == "" {
		return
	}
	// Cut on runes so a Chinese comment in the statement cannot end mid-byte.
	if r := []rune(sql); len(r) > historySQLChars {
		sql = string(r[:historySQLChars]) + " …"
	}

	entry := HistoryEntry{
		Time:          time.Now().UnixMilli(),
		ConnID:        connID,
		Database:      strings.TrimSpace(database),
		Table:         statementTable(sql),
		SQL:           sql,
		Error:         result.Error,
		RowCount:      result.RowCount,
		Affected:      result.AffectedRows,
		ExecutionTime: result.ExecutionTime,
	}

	a.historyMu.Lock()
	defer a.historyMu.Unlock()

	a.historySeq++
	entry.ID = a.historySeq
	a.history = append(a.history, entry)
	if len(a.history) > historyLimit {
		a.history = append([]HistoryEntry(nil), a.history[len(a.history)-historyLimit:]...)
	}

	// The Fyne client shares this file: fold in what it recorded meanwhile
	// before writing, so neither side's statements are lost.
	a.mergeHistory()
	a.writeHistory()
}

// History returns recorded statements newest first, optionally narrowed to one
// connection, database or table.
func (a *App) History(connID, database, table string, limit int) []HistoryEntry {
	if limit <= 0 || limit > historyLimit {
		limit = historyLimit
	}
	table = strings.ToLower(strings.TrimSpace(table))
	database = strings.ToLower(strings.TrimSpace(database))

	a.historyMu.Lock()
	defer a.historyMu.Unlock()

	entries := make([]HistoryEntry, 0, limit)
	for i := len(a.history) - 1; i >= 0 && len(entries) < limit; i-- {
		entry := a.history[i]
		if connID != "" && entry.ConnID != connID {
			continue
		}
		if table != "" && entry.Table != table {
			continue
		}
		// Statements that ran without an explicit database inherit the
		// connection's own, which the caller cannot tell apart after the fact.
		if database != "" && entry.Database != "" && strings.ToLower(entry.Database) != database {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// ClearHistory drops recorded statements, all of them or those matching the
// given connection, database and table filters. It returns how many went away.
func (a *App) ClearHistory(connID, database, table string) int {
	connID = strings.TrimSpace(connID)
	database = strings.ToLower(strings.TrimSpace(database))
	table = strings.ToLower(strings.TrimSpace(table))

	a.historyMu.Lock()
	defer a.historyMu.Unlock()

	// A filtered clear is a statement about every matching entry, including
	// ones another client recorded while we ran: merge first, then filter.
	a.mergeHistory()
	kept := a.history[:0]
	for _, entry := range a.history {
		if connID != "" && entry.ConnID != connID {
			kept = append(kept, entry)
			continue
		}
		if table != "" && entry.Table != table {
			kept = append(kept, entry)
			continue
		}
		if database != "" && entry.Database != "" && strings.ToLower(entry.Database) != database {
			kept = append(kept, entry)
			continue
		}
	}
	removed := len(a.history) - len(kept)
	if removed == 0 {
		return 0
	}
	a.history = append([]HistoryEntry(nil), kept...)
	a.writeHistory()
	return removed
}

func runSelect(db *sql.DB, query string, start time.Time) QueryResult {
	rows, err := db.Query(query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	colInfos := make([]Column, len(columns))
	for i, col := range columns {
		nullable, _ := columnTypes[i].Nullable()
		colInfos[i] = Column{
			Name:     col,
			Type:     columnTypes[i].DatabaseTypeName(),
			Nullable: nullable,
		}
	}

	var resultRows [][]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return QueryResult{Error: err.Error()}
		}

		row := make([]interface{}, len(values))
		for i, v := range values {
			switch val := v.(type) {
			case []byte:
				row[i] = string(val)
			case time.Time:
				row[i] = val.Format(time.RFC3339)
			default:
				row[i] = val
			}
		}
		resultRows = append(resultRows, row)
	}

	elapsed := time.Since(start).Seconds()
	return QueryResult{
		Columns:       colInfos,
		Rows:          resultRows,
		RowCount:      len(resultRows),
		ExecutionTime: elapsed,
	}
}

func executeCommand(db *sql.DB, query string, start time.Time) QueryResult {
	result, err := db.Exec(query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	affectedRows, _ := result.RowsAffected()
	lastInsertID, _ := result.LastInsertId()
	elapsed := time.Since(start).Seconds()
	return QueryResult{
		AffectedRows:  affectedRows,
		LastInsertID:  lastInsertID,
		ExecutionTime: elapsed,
	}
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

func createConnection(config ConnectionConfig) (*sql.DB, error) {
	var dsn string
	var driver string
	switch config.Type {
	case "mysql":
		driver = "mysql"
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s",
			config.Username, config.Password, config.Host, config.Port, config.Database)
		if config.SSLMode == "true" {
			dsn += "?tls=true"
		}
	case "postgresql":
		driver = "postgres"
		dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			config.Host, config.Port, config.Username, config.Password, config.Database, config.SSLMode)
	case "sqlite":
		driver = "sqlite3"
		dsn = config.Database
	case "sqlserver":
		driver = "sqlserver"
		encrypt := config.SSLMode
		if encrypt == "false" || encrypt == "true" {
			if encrypt == "false" {
				encrypt = "disable"
			} else {
				encrypt = "require"
			}
		}
		dsn = fmt.Sprintf("sqlserver://%s:%s@%s:%d?database=%s&encrypt=%s&TrustServerCertificate=true",
			config.Username, config.Password, config.Host, config.Port, url.QueryEscape(config.Database), encrypt)
	case "oracle":
		driver = "oracle"
		dsn = fmt.Sprintf("oracle://%s:%s@%s:%d/%s",
			config.Username, config.Password, config.Host, config.Port, config.Database)
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

func isSelectQuery(query string) bool {
	query = strings.ToUpper(strings.TrimSpace(query))
	return strings.HasPrefix(query, "SELECT") ||
		strings.HasPrefix(query, "SHOW") ||
		strings.HasPrefix(query, "DESCRIBE") ||
		strings.HasPrefix(query, "EXPLAIN")
}

// withRowLimit caps a plain SELECT — or a WITH ... SELECT CTE — the way GUI
// clients do; the shapes live in the shared SQL text package both TablePro
// clients generate from.
func withRowLimit(query string, dbType string, limit int) string {
	return tableprosql.RowLimit(dbType, query, limit)
}

// statusOK is the shared success payload of the mutating endpoints.
var statusOK = map[string]string{"status": "ok"}

// post registers one POST endpoint of the JSON API: decode the request body
// into Req, run call, and map the result onto the shared convention — 405 for
// a non-POST request, errStatus (or 400) for a body that does not decode or a
// call that fails, otherwise the encoded response. Every handler used to
// hand-write this same skeleton.
func post[Req any](a *App, mux *http.ServeMux, path string, errStatus int, call func(*App, Req) (any, error)) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var body Req
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		resp, err := call(a, body)
		if err != nil {
			writeError(w, errStatus, err.Error())
			return
		}
		writeJSON(w, resp)
	})
}

// Request shapes of the POST route table, kept in one place so the JSON
// contract stays reviewable at a glance.
type idRequest struct {
	ID string `json:"id"`
}

type connIDRequest struct {
	ConnID string `json:"connId"`
}

type connDatabaseRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
}

type tableRefRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
}

type sqlNameRequest struct {
	tableRefRequest
	Scope string `json:"scope"`
}

type queryRequest struct {
	ConnID   string `json:"connId"`
	Query    string `json:"query"`
	Database string `json:"database"`
}

type previewRequest struct {
	ConnID   string        `json:"connId"`
	Database string        `json:"database"`
	Schema   string        `json:"schema"`
	Table    string        `json:"table"`
	Limit    int           `json:"limit"`
	Offset   int           `json:"offset"`
	Filters  []TableFilter `json:"filters"`
}

type fkValuesRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Column   string `json:"column"`
	Limit    int    `json:"limit"`
}

type exportInsertRequest struct {
	ConnID   string          `json:"connId"`
	Database string          `json:"database"`
	Schema   string          `json:"schema"`
	Table    string          `json:"table"`
	Columns  []string        `json:"columns"`
	Rows     [][]interface{} `json:"rows"`
}

type exportXlsxRequest struct {
	Columns []WorkbookColumn `json:"columns"`
	Rows    [][]interface{}  `json:"rows"`
}

type tableActionRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Action   string `json:"action"`
	NewName  string `json:"newName"`
}

type historyRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Table    string `json:"table"`
	Limit    int    `json:"limit"`
}

type historyFilterRequest struct {
	ConnID   string `json:"connId"`
	Database string `json:"database"`
	Table    string `json:"table"`
}

type searchRequest struct {
	Pattern string `json:"pattern"`
	Limit   int    `json:"limit"`
}

func (a *App) startHTTPServerOnListener(listener net.Listener) {
	mux := http.NewServeMux()

	// test-connection opens, pings and closes a throwaway connection without
	// touching saved state, and logs the attempt: hand-written on purpose.
	mux.HandleFunc("/api/test-connection", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var config ConnectionConfig
		if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		log.Printf("API test-connection: type=%s host=%s:%d db=%s user=%s encrypt=%s",
			config.Type, config.Host, config.Port, config.Database, config.Username, config.SSLMode)
		db, err := createConnection(config)
		if err != nil {
			log.Printf("API createConnection error: %v", err)
			writeError(w, 400, err.Error())
			return
		}
		defer db.Close()
		if err := db.Ping(); err != nil {
			log.Printf("API db.Ping error: %v", err)
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})

	// connections and settings are the only GET+POST endpoints; the rest of
	// the surface is POST-only and lives in the route table below.
	mux.HandleFunc("/api/connections", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			conns := a.GetConnections()
			writeJSON(w, conns)
		case http.MethodPost:
			var config ConnectionConfig
			if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
				writeError(w, 400, err.Error())
				return
			}
			if err := a.AddConnection(config); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			writeJSON(w, map[string]string{"status": "ok"})
		default:
			w.WriteHeader(405)
		}
	})

	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, a.GetSettings())
		case http.MethodPost:
			var settings AppSettings
			if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
				writeError(w, 400, err.Error())
				return
			}
			if err := a.SaveSettings(settings); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			writeJSON(w, map[string]string{"status": "ok"})
		default:
			w.WriteHeader(405)
		}
	})

	post(a, mux, "/api/connections/update", 500, func(a *App, config ConnectionConfig) (any, error) {
		if err := a.UpdateConnection(config); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/connections/delete", 500, func(a *App, body idRequest) (any, error) {
		if err := a.DeleteConnection(body.ID); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/connect", 400, func(a *App, body idRequest) (any, error) {
		if err := a.Connect(body.ID); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/disconnect", 500, func(a *App, body idRequest) (any, error) {
		if err := a.Disconnect(body.ID); err != nil {
			return nil, err
		}
		return statusOK, nil
	})
	post(a, mux, "/api/databases", 400, func(a *App, body connIDRequest) (any, error) {
		return a.GetDatabases(body.ConnID)
	})
	post(a, mux, "/api/tables", 400, func(a *App, body tableRefRequest) (any, error) {
		return a.GetTables(body.ConnID, body.Database, body.Schema)
	})
	post(a, mux, "/api/schemas", 400, func(a *App, body connDatabaseRequest) (any, error) {
		return a.GetSchemas(body.ConnID, body.Database)
	})
	post(a, mux, "/api/columns", 400, func(a *App, body tableRefRequest) (any, error) {
		return a.GetColumns(body.ConnID, body.Database, body.Schema, body.Table)
	})
	post(a, mux, "/api/query-columns", 400, func(a *App, body queryRequest) (any, error) {
		return a.QueryColumns(body.ConnID, body.Query, body.Database)
	})
	post(a, mux, "/api/structure", 400, func(a *App, body tableRefRequest) (any, error) {
		return a.GetTableStructure(body.ConnID, body.Database, body.Schema, body.Table)
	})
	post(a, mux, "/api/preview", 400, func(a *App, body previewRequest) (any, error) {
		return a.Preview(body.ConnID, body.Database, body.Schema, body.Table, body.Limit, body.Offset, body.Filters), nil
	})
	post(a, mux, "/api/fk-values", 400, func(a *App, body fkValuesRequest) (any, error) {
		return a.ForeignKeyValues(body.ConnID, body.Database, body.Schema, body.Table, body.Column, body.Limit)
	})
	post(a, mux, "/api/foreign-keys", 400, func(a *App, body tableRefRequest) (any, error) {
		return a.ForeignKeys(body.ConnID, body.Database, body.Schema, body.Table)
	})
	post(a, mux, "/api/export-insert", 400, func(a *App, body exportInsertRequest) (any, error) {
		script, err := a.InsertScript(body.ConnID, body.Database, body.Schema, body.Table, body.Columns, body.Rows)
		if err != nil {
			return nil, err
		}
		return map[string]string{"sql": script}, nil
	})
	post(a, mux, "/api/export-xlsx", 400, func(a *App, body exportXlsxRequest) (any, error) {
		workbook, err := a.WorkbookBase64(body.Columns, body.Rows)
		if err != nil {
			return nil, err
		}
		return map[string]string{"base64": workbook}, nil
	})
	post(a, mux, "/api/table-action", 400, func(a *App, body tableActionRequest) (any, error) {
		statement, err := a.TableAction(body.ConnID, body.Database, body.Schema, body.Table, body.Action, body.NewName)
		if err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok", "statement": statement}, nil
	})
	post(a, mux, "/api/execute", 400, func(a *App, body queryRequest) (any, error) {
		return a.ExecuteQuery(body.ConnID, body.Query, body.Database), nil
	})
	post(a, mux, "/api/history", 400, func(a *App, body historyRequest) (any, error) {
		return a.History(body.ConnID, body.Database, body.Table, body.Limit), nil
	})
	post(a, mux, "/api/clear-history", 400, func(a *App, body historyFilterRequest) (any, error) {
		return map[string]int{"removed": a.ClearHistory(body.ConnID, body.Database, body.Table)}, nil
	})
	post(a, mux, "/api/search-objects", 400, func(a *App, body searchRequest) (any, error) {
		return a.SearchObjects(body.Pattern, body.Limit)
	})
	post(a, mux, "/api/query-source", 400, func(a *App, body queryRequest) (any, error) {
		return a.QuerySource(body.ConnID, body.Query, body.Database), nil
	})
	post(a, mux, "/api/sql-name", 400, func(a *App, body sqlNameRequest) (any, error) {
		name, err := a.SqlName(body.ConnID, body.Database, body.Schema, body.Table, body.Scope)
		if err != nil {
			return nil, err
		}
		return map[string]string{"name": name}, nil
	})
	post(a, mux, "/api/build-changes", 400, func(a *App, set ChangeSet) (any, error) {
		statements, err := a.PreviewChanges(set)
		if err != nil {
			return nil, err
		}
		return map[string][]string{"statements": statements}, nil
	})
	post(a, mux, "/api/apply-changes", 400, func(a *App, set ChangeSet) (any, error) {
		applied, err := a.ApplyChanges(set)
		if err != nil {
			return nil, err
		}
		return map[string]int64{"appliedRows": applied}, nil
	})

	go func() {
		srv := &http.Server{Handler: corsMiddleware(mux)}
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()
}

type injectHandler struct {
	inner  http.Handler
	inject string
}

func (h *injectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		h.inner.ServeHTTP(w, r)
		return
	}
	rec := &responseRecorder{ResponseWriter: w, statusCode: 200}
	h.inner.ServeHTTP(rec, r)
	if rec.statusCode != 200 {
		w.WriteHeader(rec.statusCode)
		rec.body.WriteTo(w)
		return
	}
	body := bytes.Replace(rec.body.Bytes(), []byte("<head>"), []byte("<head>"+h.inject), 1)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	return r.body.Write(b)
}

// localOriginRe matches the origins the frontend legitimately loads from:
// the packaged webview (same server, same origin — which needs no CORS header
// at all) and the vite dev server. Every other web origin is refused the CORS
// grant, so a page the user browses cannot read responses from this API —
// with the old `*` it could have run SQL against saved connections.
var localOriginRe = regexp.MustCompile(`(?i)^https?://(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$`)

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); localOriginRe.MatchString(origin) {
			// Echo the specific origin, never `*`: a wildcard grant would
			// re-open the API to every web page.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
