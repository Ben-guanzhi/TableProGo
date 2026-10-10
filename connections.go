package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
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
	DB       *sql.DB           `json:"-"`
	Config   ConnectionConfig  `json:"-"`

	poolsMu   sync.Mutex
	pools     map[string]*sql.DB
	poolOrder []string // per-database pool LRU, least-recently-used first

	// Quick-open snapshot: this connection's whole object list, rebuilt only
	// when App.catalogVersion moves past the generation it was built at.
	// The lock lets a keystroke wait on the one builder instead of starting a
	// second catalog walk. catalogBuilds counts rebuilds and is pinned by
	// tests so the "answered from memory" claim stays true.
	catalogMu     sync.Mutex
	catalog       catalogSnapshot
	catalogBuilds int
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

func (a *App) connection(connID string) (*Connection, error) {
	a.connectionsMu.RLock()
	defer a.connectionsMu.RUnlock()

	conn, ok := a.connections[connID]
	if !ok || conn.DB == nil {
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
