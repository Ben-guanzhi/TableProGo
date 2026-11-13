package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"sync/atomic"
	"tablepro-go/internal/mcp"
	"tablepro-go/internal/txn"
	"tablepro-go/internal/workbenchhost"

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
	// workbench is the W-T2 host runner: the plugin fibers (navop-ssh terminal),
	// the key surface (nav.nodes / terminal.channels / connections.store) and the
	// broadcast gate live here. Nil-safe at every API entry.
	workbench *workbenchhost.Runner
	// workbenchOrch owns the plugin registry when more than one plugin runs
	// (W-C1: navop-ssh provider + navop-containers consumer over one store).
	// workbench is the provider runner, kept as the terminal/files entry point.
	workbenchOrch *workbenchhost.Orchestrator
	// passcodePath is the argon2id store's location (P2-9); a field rather
	// than a computed constant so tests point it at a temp dir.
	passcodePath  string
	settingsMu    sync.RWMutex
	history       []HistoryEntry
	historyPath   string
	favoritesPath string
	favoritesMu   sync.Mutex
	favorites     []FavoriteEntry
	historySeq    int64
	historyMu     sync.Mutex
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

	// planSeq is a monotonic counter for compare-plan ids. Not time-based:
	// back-to-back storeComparePlan calls on Windows can share a nanosecond,
	// and the later plan would overwrite the earlier one.
	planSeq int64

	// W6: row-level sync plans, held between the analysis that shows them and
	// the apply that runs the ticked changes. Addressed by id so the client
	// selects changes rather than handing values back for execution.
	rowSyncPlans map[string]*rowSyncPlan
	rowSyncMu    sync.Mutex
	rowSyncSeq   int64

	// runs holds the cancel funcs of statements still executing, keyed by
	// connection. A connection can have more than one in flight (a tab switch
	// starts the next statement before the previous returns), so each keeps a
	// set rather than a single handle. CancelQuery drains it.
	runsMu sync.Mutex
	runs   map[string]map[int64]*runEntry
	runSeq atomic.Int64

	// mcpConfirmer asks the UI to approve one MCP write (mcp.go). Nil means the MCP
	// server is read-only: an unattended write path is exactly what it refuses.
	mcpConfirmerMu sync.Mutex
	mcpConfirmer   func(connName, sql string) bool
	// mcpHTTPServer is the HTTP transport's persistent server: the initialized
	// handshake must outlive one request for tools/list to be answerable. It is
	// rebuilt when the settings it was built from change.
	mcpHTTPMu      sync.Mutex
	mcpHTTPServer  *mcp.Server
	mcpHTTPMaxRows int
	mcpHTTPWrites  bool

	// txSessions is the interactive transaction each tab holds (txn.go). Keyed by
	// tab id so two tabs on one connection keep separate transactions.
	txSessionsMu sync.Mutex
	txSessions   map[string]*txn.Session
	// txSweeperStop closes to stop the transaction-deadline sweeper
	// (startTxSweeper). Nil before the app starts; the sweeper goroutine
	// selects on it between ticks. Guarded by txSweeperMu: stopTxSweeper nils
	// it, and the field must not be read unsynchronized.
	txSweeperMu   sync.Mutex
	txSweeperStop chan struct{}

	// catalogGenerations bumps on every statement that could have changed a
	// catalog. The quick-open snapshot (search.go) is rebuilt only when the
	// connection's own generation moves; between bumps, a keystroke is answered
	// from memory. The counter is PER CONNECTION: a global one made a DDL on
	// connection A invalidate connection B's snapshot, which is exactly the
	// cross-connection coupling a per-connection cache must not have (TP-21).
	catalogGenMu sync.Mutex
	catalogGen   map[string]uint64

	// httpSrv is the API server started by startHTTPServerOnListener. Closing
	// it at shutdown stops accepting requests and releases the listener/port;
	// leaving it running means the process keeps a bound socket and a live
	// request handler while it is supposed to be gone (TP-08).
	httpSrvMu sync.Mutex
	httpSrv   *http.Server

	// reclaimer tracks large documents (compare plans, transaction sessions)
	// and reclaims their memory when the client goes idle. Modeled on
	// TablePlus' LargeDocumentMemoryReclaimer (idlereclaim.go).
	reclaimer *Reclaimer

	// apiToken guards the loopback JSON API. It is minted once per process and
	// handed to the webview in the injected <script>, so a page that merely
	// happens to run on another localhost port cannot reach it.
	apiToken string

	// httpGate is a semaphore limiting in-flight HTTP requests. Created with
	// the app, not lazily: a lazy init inside the handler would race when two
	// requests arrive before the first server response (both could observe
	// nil and create two channels).
	httpGate chan struct{}
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
	// IdleLockMinutes auto-locks the app (and disconnects everything) after
	// this many minutes without keyboard/mouse input; 0 turns the idle lock off.
	// Only meaningful once a passcode is set — a lock nothing can unlock is a
	// dead end, so the UI and the frontend watcher both gate on HasPasscode.
	IdleLockMinutes int `json:"idleLockMinutes"`
	// LockOnHide locks immediately when the window is hidden or backgrounded.
	LockOnHide bool `json:"lockOnHide"`
	// LockOnSleep locks when the wall clock jumps far past the watcher's tick,
	// which means the machine slept (timers freeze) rather than the user
	// idled — the one case in-page activity tracking gets wrong on its own.
	LockOnSleep bool `json:"lockOnSleep"`
	// PrivacyMode blurs connection identity and result cells for screen sharing
	// and recording (W8). It is a DISPLAY filter only: nothing stops being sent to
	// the database, and the masking is undone on hover — it is there to keep a
	// bystander or a capture from reading names and rows, not to hide anything from
	// the person using the app.
	PrivacyMode bool `json:"privacyMode"`
	// TxTimeoutMinutes is the absolute deadline for interactive transactions
	// (DESIGN-txn-timeout-2026-11-03, policy B): a transaction still open this
	// many minutes after BEGIN is rolled back by the sweeper. 0 disables the
	// governance entirely — the pre-existing unbounded behavior. The default
	// (10) matches db-dev's manual-transaction timeout, the reference
	// implementation the design aligns on.
	TxTimeoutMinutes int `json:"txTimeoutMinutes"`
	// TxWarnAheadMinutes is how long before the deadline the UI starts
	// warning. 0 uses the built-in ratio (a sixth of the timeout).
	TxWarnAheadMinutes int `json:"txWarnAheadMinutes"`
	// VimMode turns the editor's modal keybindings on (W2-b). Off by default:
	// Vim is a preference of a subset of users, and its Normal mode makes the
	// editor feel broken to anyone who expected to type immediately.
	VimMode bool `json:"vimMode"`
	// FormatPreset names the SQL layout the editor's format action applies
	// (W2-c): "default" (clause per line), "compact" (minimal breaks) or
	// "expanded" (one select item per line, wider indents). Empty means
	// "default", so an older settings file keeps formatting as before.
	FormatPreset string `json:"formatPreset"`
}

// defaultResultLimit matches what GUI clients hand out before the user says
// otherwise: enough rows to inspect a table, few enough to stay instant.
const defaultResultLimit = 1000

// defaultTheme follows the OS, which is what every desktop database client
// shipped in recent years does.
const defaultTheme = "system"

// defaultTxTimeoutMinutes is the interactive transaction's absolute deadline
// (DESIGN-txn-timeout-2026-11-03): 10 minutes, matching db-dev's manual
// transaction — the reference implementation the design aligns on.
const defaultTxTimeoutMinutes = 10

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
//
// TABLEPRO_HOME overrides it. Two callers need that: tests (which must never
// write into the developer's real profile — a lesson this workspace already paid
// for once) and portable installs (a USB stick wants its state beside it).
func userDataDir() string {
	if dir := strings.TrimSpace(os.Getenv("TABLEPRO_HOME")); dir != "" {
		return dir
	}
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

func passcodeFilePath() string { return userDataFilePath("passcode.json") }

func historyFilePath() string { return userDataFilePath("history.json") }

func NewApp() *App {
	return newAppWithPaths("", "")
}

// newAppWithPaths is NewApp with the two overridable locations (connections
// file and passcode store) taken from the CLI when given; the defaults are
// the user-data-dir ones.
func newAppWithPaths(configPath, passcodePath string) *App {
	a := &App{
		connections:   make(map[string]*Connection),
		filePath:      connectionsFilePath(),
		settingsPath:  settingsFilePath(),
		passcodePath:  passcodePath,
		historyPath:   historyFilePath(),
		favoritesPath: favoritesFilePath(),
		reclaimer:     NewReclaimer(),
		httpGate:      make(chan struct{}, httpMaxConcurrentRequests),
	}
	if configPath != "" {
		a.filePath = configPath
	}
	// The comment above always CLAIMED this default; the code did not apply it, so a
	// run without --passcode handed every passcode operation an empty path: setting one
	// failed, HasPasscode() answered false, and the idle lock could never engage. The
	// server form surfaced it (it refuses to start without a passcode, and could never
	// find one).
	if passcodePath != "" {
		a.passcodePath = passcodePath
	} else {
		a.passcodePath = userDataFilePath("passcode.json")
	}
	a.loadFromFile()
	a.loadSettings()
	a.loadHistory()
	a.loadFavorites()
	a.reclaimer.Start()
	a.startTxSweeper()
	// W-T2/W-C1: the orchestrator owns the plugin registry. TWO plugins may run
	// over ONE shared store: navop-ssh (provider: terminal/files/tunnel.proxy)
	// and navop-containers (consumer: container.session, depends tunnel.proxy).
	// One registry, two fibers — that is what makes "kill the provider and the
	// consumer deactivates first" hold (Thm 70's ¬relied guard). The host binds
	// connections.store itself (AD-6: single writer = the host kernel) — the
	// archive carries NO secrets, only the ids/labels a terminal tab needs
	// (AD-4: secret values never enter the key surface).
	a.workbenchOrch = workbenchhost.NewOrchestrator()
	a.workbench = a.workbenchOrch.SSH()
	if err := a.workbench.ProvideConnectionsArchive(a.connectionsArchive()); err != nil {
		log.Printf("workbench: provide connections.store: %v", err)
	}
	// Plugin discovery follows the pluginreg convention: one environment variable
	// per plugin. A missing plugin is the NORMAL case (工作台能力默认不接入) — the
	// fiber simply never exists, and the UI shows no terminal tab.
	if p := os.Getenv("TABLEPRO_WORKBENCH_NAVOP_SSH"); p != "" {
		if err := a.workbenchOrch.LaunchProvider(p); err != nil {
			log.Printf("workbench: launch navop-ssh: %v", err)
		}
	}
	// The consumer is optional too: only a configured navop-containers binary
	// starts a second fiber. Its banner must declare tunnel.proxy (the
	// orchestrator refuses a mis-wired build) and it stays Inactive until the
	// provider is Active.
	if p := os.Getenv("TABLEPRO_WORKBENCH_NAVOP_CONTAINERS"); p != "" {
		if err := a.workbenchOrch.LaunchConsumer(p); err != nil {
			log.Printf("workbench: launch navop-containers: %v", err)
		}
	}
	return a
}

// connectionsArchive is what the host publishes under connections.store for the
// workbench plugins: id/name/type/host per saved connection — labels and routing,
// NEVER credentials (AD-4).
func (a *App) connectionsArchive() []map[string]any {
	a.connectionsMu.RLock()
	defer a.connectionsMu.RUnlock()
	out := make([]map[string]any, 0, len(a.savedConnections))
	for _, c := range a.savedConnections {
		out = append(out, map[string]any{
			"id":   c.ID,
			"name": c.Name,
			"type": c.Type,
			"host": c.Host,
		})
	}
	return out
}

func (a *App) loadSettings() {
	a.settings.ResultLimit = defaultResultLimit
	a.settings.Theme = defaultTheme
	a.settings.SafeMode = safeModeOff
	a.settings.LockOnSleep = true // db-dev's default: a sleep jump locks unless told not to
	a.settings.TxTimeoutMinutes = defaultTxTimeoutMinutes

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
		IdleLockMinutes    *int    `json:"idleLockMinutes"`
		LockOnHide         *bool   `json:"lockOnHide"`
		LockOnSleep        *bool   `json:"lockOnSleep"`
		PrivacyMode        *bool   `json:"privacyMode"`
		TxTimeoutMinutes   *int    `json:"txTimeoutMinutes"`
		TxWarnAheadMinutes *int    `json:"txWarnAheadMinutes"`
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
	if stored.IdleLockMinutes != nil && *stored.IdleLockMinutes >= 0 {
		a.settings.IdleLockMinutes = *stored.IdleLockMinutes
	}
	if stored.LockOnHide != nil {
		a.settings.LockOnHide = *stored.LockOnHide
	}
	if stored.LockOnSleep != nil {
		a.settings.LockOnSleep = *stored.LockOnSleep
	}
	if stored.PrivacyMode != nil {
		a.settings.PrivacyMode = *stored.PrivacyMode
	}
	if stored.TxTimeoutMinutes != nil && *stored.TxTimeoutMinutes >= 0 {
		a.settings.TxTimeoutMinutes = *stored.TxTimeoutMinutes
	}
	if stored.TxWarnAheadMinutes != nil && *stored.TxWarnAheadMinutes >= 0 {
		a.settings.TxWarnAheadMinutes = *stored.TxWarnAheadMinutes
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
		if conns[i].SSH.Passphrase != "" && !isSealedValue(conns[i].SSH.Passphrase) {
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
		// The key passphrase rides the same layer (ssh.go's contract).
		sshPassphrase, err := unprotectSecret(conns[i].SSH.Passphrase)
		if err != nil {
			log.Printf("Connection %s (ssh key): %v", conns[i].ID, err)
			sshPassphrase = ""
		}
		conns[i].SSH.Passphrase = sshPassphrase
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
		// The key passphrase is a credential too, and ssh.go's contract says
		// it goes through this same layer. Leaving it out would write it in
		// clear text next to two sealed secrets — the same class of gap as
		// the .bak leak fixed for fynequery.
		sshPassphrase, err := protectSecret(sealed[i].SSH.Passphrase)
		if err != nil {
			log.Printf("Failed to encrypt ssh key passphrase for %s: %v", sealed[i].ID, err)
			return
		}
		sealed[i].SSH.Passphrase = sshPassphrase
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
			if pw, err := unprotectSecret(c.SSH.Passphrase); err == nil {
				c.SSH.Passphrase = pw
			} else {
				log.Printf("Connection %s (ssh key): %v", c.ID, err)
				c.SSH.Passphrase = ""
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
	// Stop the API server first: while it is serving, requests can still open
	// connections and check out pools, racing the teardown below.
	a.closeHTTPServer()
	// W-T2/W-C1: retire the workbench fibers BEFORE the pools go away — the
	// plugin processes are separate executables and outlive nothing, but their
	// accumulator must still run so the key surface clears (Cor 69). The
	// orchestrator stops the CONSUMER first (its teardown must not race a
	// provider that would then wait on the ¬relied guard), then the provider.
	if a.workbenchOrch != nil {
		a.workbenchOrch.Stop()
	} else if a.workbench != nil {
		a.workbench.Stop()
	}
	a.stopTxSweeper()
	if a.reclaimer != nil {
		a.reclaimer.Stop()
	}
	// Roll back and drop every tab transaction BEFORE the pools and drivers go
	// away: forgetSession needs a live connection to roll back against, and it
	// takes connectionsMu on the read path, so it must run outside the write
	// lock below. Leaving the sessions behind would leak every open tab's
	// server-side transaction (TP-04).
	a.forgetAllSessions()
	a.connectionsMu.Lock()
	defer a.connectionsMu.Unlock()
	for id, conn := range a.connections {
		// Single inverse of Connect (TP-01): this now also rolls back the tab
		// transactions and closes the SSH tunnel, which the hand-written list
		// here used to omit.
		conn.close(id)
		delete(a.connections, id)
	}
	log.Println("TablePro Go shutdown")
}
