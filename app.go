package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"

	"sync/atomic"
	"tablepro-go/internal/txn"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/sijms/go-ora/v2"
)

type App struct {
	ctx              context.Context
	connections      map[string]*Connection
	connectionsMu    sync.RWMutex
	currentConnID    string
	savedConnections []ConnectionConfig
	connFilterCache  map[string]DatabaseFilter // connID -> DatabaseFilter 缓存（CLRS ch11 哈希表）
	filePath         string
	settings         AppSettings
	settingsPath     string
	settingsMu       sync.RWMutex
	history          []HistoryEntry
	historyPath      string
	favoritesPath    string
	favoritesMu      sync.Mutex
	favorites        []FavoriteEntry
	historySeq       int64
	historyMu        sync.Mutex
	// histObserved is the history file as this process last read or wrote it;
	// mergeHistory folds in only what other clients added since then.
	histObserved []HistoryEntry
	// connObserved is the connections file as this process last read or wrote it
	// it; saveToFile folds in only what other clients added since then.
	connObserved []ConnectionConfig

	// comparePlans holds a computed schema comparison between the call that
	// shows it and the call that applies it, so the client selects entry ids
	// rather than handing SQL back for execution.
	comparePlans map[string]*comparePlan
	compareMu    sync.Mutex

	// runs holds the cancel funcs of statements still executing, keyed by
	// connection. A connection can have more than one in flight (a tab switch
	// starts the next statement before the previous returns), so each keeps a
	// set rather than a single handle. CancelQuery drains it.
	runsMu sync.Mutex
	runs   map[string]map[int64]context.CancelFunc
	runSeq atomic.Int64

	// mcpConfirmer asks the UI to approve one MCP write (mcp.go). Nil means the MCP
	// server is read-only: an unattended write path is exactly what it refuses.
	mcpConfirmerMu sync.Mutex
	mcpConfirmer   func(connName, sql string) bool

	// txSessions is the interactive transaction each tab holds (txn.go). Keyed by
	// tab id so two tabs on one connection keep separate transactions.
	txSessionsMu sync.Mutex
	txSessions   map[string]*txn.Session

	// catalogVersion bumps on every statement that could have changed a
	// catalog. The quick-open snapshot (search.go) is rebuilt only when this
	// moves; between bumps, a keystroke is answered from memory.
	catalogVersion atomic.Uint64

	// apiToken guards the loopback JSON API. It is minted once per process and
	// handed to the webview in the injected <script>, so a page that merely
	// happens to run on another localhost port cannot reach it.
	apiToken string
}

// AppSettings holds UI preferences, persisted next to the connection store.
type AppSettings struct {
	ShowColumnComments bool `json:"showColumnComments"`
	// ResultLimit caps how many rows a SELECT returns; 0 means no cap.
	ResultLimit int `json:"resultLimit"`
	// Theme is "system", "light" or "dark"; the front end resolves "system"
	// against the OS setting, so the backend only stores the choice.
	Theme string `json:"theme"`
	// SafeMode gates writing statements in the query editor (see safemode.go):
	// "off", "confirmWrites", "confirmNoWhere" or "blockNoWhere".
	SafeMode string `json:"safeMode"`
	// MCPAllowWrites lets an MCP client run DML/DDL. It is off by default and, even
	// when on, every statement is confirmed through the UI (mcp.go). Without a UI
	// attached the write is refused regardless.
	MCPAllowWrites bool `json:"mcpAllowWrites"`
	// MCPMaxRows caps how many rows one MCP query returns; 0 uses the default.
	MCPMaxRows int `json:"mcpMaxRows"`
	// ExportMaxRows caps how many rows one export (INSERT script / XLSX)
	// renders; 0 uses the built-in default.
	ExportMaxRows int `json:"exportMaxRows"`
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

func settingsFilePath() string { return userDataFilePath("settings.json") }

func historyFilePath() string { return userDataFilePath("history.json") }

func NewApp() *App {
	a := &App{
		connections:   make(map[string]*Connection),
		filePath:      connectionsFilePath(),
		settingsPath:  settingsFilePath(),
		historyPath:   historyFilePath(),
		favoritesPath: favoritesFilePath(),
	}
	a.loadFromFile()
	a.loadSettings()
	a.loadHistory()
	a.loadFavorites()
	return a
}

func (a *App) loadSettings() {
	a.settings.ResultLimit = defaultResultLimit
	a.settings.Theme = defaultTheme
	a.settings.SafeMode = safeModeOff

	data, err := os.ReadFile(a.settingsPath)
	if err != nil {
		return
	}
	// Every field is read through a pointer so an explicit zero value (0 =
	// no limit, "off" safe mode) survives, while a field absent from a file
	// written by an older version falls back to the default set above.
	var stored struct {
		ShowColumnComments *bool   `json:"showColumnComments"`
		ResultLimit        *int    `json:"resultLimit"`
		Theme              *string `json:"theme"`
		SafeMode           *string `json:"safeMode"`
		MCPAllowWrites     *bool   `json:"mcpAllowWrites"`
		MCPMaxRows         *int    `json:"mcpMaxRows"`
		ExportMaxRows      *int    `json:"exportMaxRows"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		log.Printf("Failed to load settings: %v", err)
		return
	}
	if stored.ShowColumnComments != nil {
		a.settings.ShowColumnComments = *stored.ShowColumnComments
	}
	if stored.ResultLimit != nil {
		a.settings.ResultLimit = *stored.ResultLimit
	}
	if stored.Theme != nil {
		a.settings.Theme = normalizeTheme(*stored.Theme)
	}
	if stored.SafeMode != nil {
		a.settings.SafeMode = normalizeSafeMode(*stored.SafeMode)
	}
	if stored.MCPAllowWrites != nil {
		a.settings.MCPAllowWrites = *stored.MCPAllowWrites
	}
	if stored.MCPMaxRows != nil {
		a.settings.MCPMaxRows = *stored.MCPMaxRows
	}
	if stored.ExportMaxRows != nil {
		a.settings.ExportMaxRows = *stored.ExportMaxRows
	}
}

// effectiveExportMaxRows is the user's export cap; 0 keeps the built-in
// default. The XLSX cap rides on it at ten times, matching the original
// ratio between the two formats.
func (a *App) effectiveExportMaxRows() int {
	if n := a.GetSettings().ExportMaxRows; n > 0 {
		return n
	}
	return insertExportMaxRows
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
	settings.SafeMode = normalizeSafeMode(settings.SafeMode)

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
		if conns[i].Password != "" && !isSealedValue(conns[i].Password) {
			needsSealing = true
		}
		if conns[i].SSH.Password != "" && !isSealedValue(conns[i].SSH.Password) {
			needsSealing = true
		}
		password, err := unprotectSecret(conns[i].Password)
		if err != nil {
			// A password from another Windows account or machine cannot be
			// opened here; keep the connection so its other settings survive,
			// and let the user retype it.
			log.Printf("Connection %s: %v", conns[i].ID, err)
			password = ""
		}
		conns[i].Password = password
		// The SSH password is a credential too; it is sealed with the same
		// DPAPI/keyring layer so it never lands in the JSON in clear text.
		sshPassword, err := unprotectSecret(conns[i].SSH.Password)
		if err != nil {
			log.Printf("Connection %s (ssh): %v", conns[i].ID, err)
			sshPassword = ""
		}
		conns[i].SSH.Password = sshPassword
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
		sshPassword, err := protectSecret(sealed[i].SSH.Password)
		if err != nil {
			log.Printf("Failed to encrypt ssh password for %s: %v", sealed[i].ID, err)
			return
		}
		sealed[i].SSH.Password = sshPassword
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
	disk, diskOK, diskErr := readJSONFile[ConnectionConfig](a.filePath)
	if diskErr != nil {
		log.Printf("Failed to load connections for merging: %v", diskErr)
	}
	if diskOK {
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
			// load time, a password that cannot be opened here (sealed on
			// another machine or account) is left empty for the user to retype.
			if pw, err := unprotectSecret(c.Password); err == nil {
				c.Password = pw
			} else {
				log.Printf("Connection %s: %v", c.ID, err)
				c.Password = ""
			}
			if pw, err := unprotectSecret(c.SSH.Password); err == nil {
				c.SSH.Password = pw
			} else {
				log.Printf("Connection %s (ssh): %v", c.ID, err)
				c.SSH.Password = ""
			}
			a.savedConnections = append(a.savedConnections, c)
		}
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
	// The keyring entries the previous file pointed at that this save dropped
	// (deleted connection, changed or cleared password) are now orphans.
	if diskOK {
		secretsGC(passwordsOf(disk), passwordsOf(sealed))
	}
	a.connObserved = append([]ConnectionConfig(nil), sealed...)
	// The fold-in above may have appended connections from another client,
	// which could have different filters than what we cached. Clear the
	// filter cache to be safe.
	a.invalidateConnFilterCacheLocked()
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
		conn.closeKV()
		conn.closePools()
		delete(a.connections, id)
	}
	log.Println("TablePro Go shutdown")
}
