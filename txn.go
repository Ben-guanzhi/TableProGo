package main

// Interactive transactions: one open transaction per tab, on top of
// dbcore.TxManager.
//
// The client's side of the state machine lives in internal/txn (which transaction
// a tab has open, the database it is bound to, and what to do after a statement
// fails). This file is the wiring: the managers themselves, the transaction the
// query path uses when one is open, and the three API calls.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/dbcore/dbcore"

	"tablepro-go/internal/txn"
)

// txns holds one TxManager per connection+database pair. The manager owns the
// *sql.Tx; a fresh manager per pair keeps a transaction from outliving the pool it
// was opened on (the pool is evicted or closed independently).
//
// A manager wraps a driver built from a *sql.DB, so it must not survive that DB: the
// connection's close calls closeTxns, and a reconnect starts a new one.
var txns = struct {
	mu sync.Mutex
	m  map[string]txnManager
}{m: map[string]txnManager{}}

func txKey(connID, database string) string { return connID + "|" + database }

// txnManager is the transaction-management surface TableProGo needs. Both the
// kernel's TxManager (for compiled-in engines) and the plugin session adapter
// (for plugin-backed engines) implement it, so txManagerFor can return either
// without callers knowing which.
type txnManager interface {
	Begin(ctx context.Context, mode dbcore.TxMode) (*dbcore.TransactionInfo, error)
	Commit(id string) error
	Rollback(id string) error
	ExecuteInTx(ctx context.Context, id, query string, args ...dbcore.Value) (*dbcore.QueryResult, error)
	QueryInTx(ctx context.Context, id string, query string, args ...dbcore.Value) (*dbcore.TxRows, error)
	Close()
	RollbackAll()
}

// sessionTxManager adapts a plugin connection's SessionTx to the txnManager
// interface. The plugin owns the transaction internally; this adapter only
// drives Begin/Commit/Rollback through the SessionTx surface and routes
// statement execution through the session (which runs inside the plugin's
// transaction).
type sessionTxManager struct {
	sessTx dbcore.SessionTx
	sess   dbcore.SQLDriver
}

func (m *sessionTxManager) Begin(ctx context.Context, mode dbcore.TxMode) (*dbcore.TransactionInfo, error) {
	if err := m.sessTx.BeginTx(ctx); err != nil {
		return nil, err
	}
	return &dbcore.TransactionInfo{ID: fmt.Sprintf("plugin-tx-%d", time.Now().UnixNano()), Status: dbcore.TxnActive}, nil
}

func (m *sessionTxManager) Commit(id string) error {
	return m.sessTx.CommitTx(context.Background())
}

func (m *sessionTxManager) Rollback(id string) error {
	return m.sessTx.RollbackTx(context.Background())
}

func (m *sessionTxManager) ExecuteInTx(ctx context.Context, id, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	// The plugin's transaction lives inside the plugin; Execute runs on the
	// session, which is already inside the transaction.
	return m.sess.Execute(ctx, query, args...)
}

func (m *sessionTxManager) QueryInTx(ctx context.Context, id string, query string, args ...dbcore.Value) (*dbcore.TxRows, error) {
	// TxRows embeds *sql.Rows, which a plugin connection does not have: the
	// plugin streams results through its own protocol, not database/sql rows.
	// Streaming on a plugin transaction is not supported; the non-streaming
	// ExecuteInTx path works because the plugin produces QueryResult directly.
	return nil, fmt.Errorf("%w: plugin transactions do not support streaming queries", dbcore.ErrNotSupported)
}

func (m *sessionTxManager) Close() {
	if m.sessTx.InTransaction() {
		_ = m.sessTx.RollbackTx(context.Background())
	}
}

func (m *sessionTxManager) RollbackAll() {
	if m.sessTx.InTransaction() {
		// The inverse of Begin. A failed rollback must not be swallowed: the
		// server-side resources (locks, the transaction's connection) would
		// leak with nothing telling the user why (TP-25).
		if err := m.sessTx.RollbackTx(context.Background()); err != nil {
			log.Printf("roll back plugin transaction: %v", err)
		}
	}
}

// database. Callers must hold no locks: building the driver can open a pool.
//
// An empty database means "the connection's own", which is what the statement paths
// pass when the tab's picker is untouched. It is resolved here so Begin and later
// statements address the same registry: keying on the raw name would build a second
// manager and lose the transaction ("transaction not found").
func (a *App) txManagerFor(conn *Connection, database string) (txnManager, error) {
	if database == "" {
		database = conn.Database
	}
	key := txKey(conn.ID, database)

	txns.mu.Lock()
	if m, ok := txns.m[key]; ok {
		txns.mu.Unlock()
		return m, nil
	}
	txns.mu.Unlock()

	drv, err := a.execDriver(conn, database)
	if err != nil {
		return nil, err
	}
	// Plugin-backed engines: the driver implements SessionTx (transaction lives
	// inside the plugin), not SQLTxDriver. Wrap it in a sessionTxManager so the
	// same per-tab transaction flow works for plugin engines. The check is
	// pluginConnection-gated because built-in drivers may also implement
	// SessionTx (they implement SQLTxDriver which covers the same surface).
	if pluginConnection(conn) {
		if sessTx, ok := drv.(dbcore.SessionTx); ok {
			m := &sessionTxManager{sessTx: sessTx, sess: drv}
			txns.mu.Lock()
			if existing, ok := txns.m[key]; ok {
				txns.mu.Unlock()
				m.Close()
				return existing, nil
			}
			txns.m[key] = m
			txns.mu.Unlock()
			return m, nil
		}
	}
	txDrv, ok := drv.(dbcore.SQLTxDriver)
	if !ok {
		return nil, fmt.Errorf("unsupported type: %s", conn.Type)
	}
	m := dbcore.NewTxManager(txDrv)

	txns.mu.Lock()
	// Another tab may have built one in the meantime; keep the first so both tabs
	// address the same registry.
	if existing, ok := txns.m[key]; ok {
		txns.mu.Unlock()
		m.Close()
		return existing, nil
	}
	txns.m[key] = m
	txns.mu.Unlock()
	return m, nil
}

// closeTxns drops every manager of one connection; called when it disconnects so a
// forgotten transaction cannot keep holding server-side resources.
func closeTxns(connID string) {
	txns.mu.Lock()
	defer txns.mu.Unlock()
	prefix := connID + "|"
	for key, m := range txns.m {
		if strings.HasPrefix(key, prefix) {
			// Roll open transactions back before dropping the manager: Close
			// only stops the sweeper, and a still-open transaction keeps its
			// dedicated pool connection checked out — DB.Close alone cannot
			// reclaim it (on SQLite the file stays locked until the
			// transaction goes away). A failed rollback is logged inside
			// RollbackAll rather than swallowed (TP-25).
			m.RollbackAll()
			m.Close()
			delete(txns.m, key)
		}
	}
}

// txOpener binds one tab's session to the App's transaction managers.
type txOpener struct {
	app   *App
	conn  *Connection
	owner *txn.Session
	db    string
	// isolation overrides the connection's configured level for this one
	// transaction (TXN-03: the backend always wired the level, but no caller
	// could choose per transaction — the only knob was the connection dialog).
	isolation string
}

func (o txOpener) Begin(database string) (string, error) {
	m, err := o.app.txManagerFor(o.conn, database)
	if err != nil {
		return "", err
	}
	// Pass the connection's configured isolation level to the driver. Without
	// this, every transaction uses the engine's default (PG read committed,
	// MySQL repeatable read, etc.) with no way for the user to choose or see it.
	info, err := m.Begin(context.Background(), dbcore.TxMode{IsolationLevel: o.conn.Config.IsolationLevel})
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

// IsolationLevel is the connection's configured level, read back onto the
// session's status line ("" = the engine's default).
func (o txOpener) IsolationLevel() string {
	if o.isolation != "" {
		return o.isolation
	}
	return o.conn.Config.IsolationLevel
}

func (o txOpener) Commit(id string) error {
	m, err := o.app.txManagerFor(o.conn, o.owner.DatabaseName())
	if err != nil {
		return err
	}
	return m.Commit(id)
}

func (o txOpener) Rollback(id string) error {
	m, err := o.app.txManagerFor(o.conn, o.owner.DatabaseName())
	if err != nil {
		return err
	}
	return m.Rollback(id)
}

// sessionOf returns the tab's transaction session, creating it on first use. Tabs
// carry it so two tabs on one connection keep separate transactions.
func (a *App) sessionOf(tabID string) *txn.Session {
	a.txSessionsMu.Lock()
	defer a.txSessionsMu.Unlock()
	if a.txSessions == nil {
		a.txSessions = map[string]*txn.Session{}
	}
	s, ok := a.txSessions[tabID]
	if !ok {
		s = &txn.Session{}
		a.txSessions[tabID] = s
		if a.reclaimer != nil {
			a.reclaimer.SetActive(s, true, sessionBytes(s), true)
		}
	}
	return s
}

// sessionBytes estimates the session's memory footprint. Transaction state is
// usually small (a few hundred bytes of SQL text), but batched writes can grow.
func sessionBytes(s *txn.Session) int64 {
	// A session holds at most a few KB of state. Use a modest constant so that
	// many sessions together still participate in the active-bytes accounting.
	return 4096
}

// ForgetTxSession is the tab-close entry point (api.go /api/tx-forget):
// forgetSession was fully implemented but had zero callers, so closing a tab
// left its transaction running server-side until disconnect.
func (a *App) ForgetTxSession(tabID string) {
	a.forgetSession(tabID)
}

// forgetSessionTabsFor lists the tab IDs whose session belongs to connID.
func (a *App) forgetSessionTabsFor(connID string) []string {
	a.txSessionsMu.Lock()
	defer a.txSessionsMu.Unlock()
	var out []string
	for tabID, s := range a.txSessions {
		if s.ConnectionID() == connID {
			out = append(out, tabID)
		}
	}
	return out
}

// forgetAllSessions drops every tab's transaction state. Shutdown calls it
// before tearing the pools and drivers down: forgetSession rolls a still-open
// transaction back, which needs a live connection, so it must run first.
// Leaving txSessions behind would leak the server-side transaction of every
// open tab (TP-04).
func (a *App) forgetAllSessions() {
	a.txSessionsMu.Lock()
	tabIDs := make([]string, 0, len(a.txSessions))
	for tabID := range a.txSessions {
		tabIDs = append(tabIDs, tabID)
	}
	a.txSessionsMu.Unlock()
	for _, tabID := range tabIDs {
		a.forgetSession(tabID)
	}
}

// forgetSession drops a tab's transaction state (the tab was closed). An open
// transaction is rolled back first: closing a tab must not leave one running.
func (a *App) forgetSession(tabID string) {
	a.txSessionsMu.Lock()
	s, ok := a.txSessions[tabID]
	delete(a.txSessions, tabID)
	a.txSessionsMu.Unlock()
	if a.reclaimer != nil && ok {
		a.reclaimer.SetActive(s, false, 0, true)
		a.reclaimer.Release(sessionBytes(s))
	}
	if !ok || !s.HasTx() {
		return
	}
	conn, err := a.connection(s.ConnectionID())
	if err != nil {
		return
	}
	_ = s.Rollback(txOpener{app: a, conn: conn, owner: s})
}

// txSession returns the tab's session, or a fresh autocommit session when the tab
// has none. A nil session would make every guard a nil check in the query path.
func (a *App) txSession(tabID string) *txn.Session {
	if tabID == "" {
		return &txn.Session{}
	}
	return a.sessionOf(tabID)
}

// errorOf renders a result's error for the session record; a successful result
// records "no error" so the statement still counts.
func errorOf(result QueryResult) error {
	if result.Error == "" {
		return nil
	}
	return errors.New(result.Error)
}

// BeginTransaction opens a transaction for a tab. The database defaults to the
// connection's own, matching how a statement without a picker resolves. The
// absolute deadline (DESIGN-txn-timeout-2026-11-03, policy B) rides on the
// settings: 0 minutes disables governance, anything else fixes
// deadline = now + timeout at Begin — statements never extend it.
func (a *App) BeginTransaction(connID, tabID, database string) (string, error) {
	return a.BeginTransactionIsolated(connID, tabID, database, "")
}

// validIsolation reports whether s is one of the kernel's isolation-level
// spellings (dbcore.TxMode.IsolationLevel). Anything else would silently
// degrade to the engine default inside TxOptionsFromMode, which is exactly the
// invisibility TXN-03 was about.
func validIsolation(s string) bool {
	switch s {
	case "read_uncommitted", "read_committed", "repeatable_read", "serializable":
		return true
	}
	return false
}

// BeginTransactionIsolated opens a transaction with an explicit isolation level;
// an empty level falls back to the connection's configured one.
func (a *App) BeginTransactionIsolated(connID, tabID, database, isolation string) (string, error) {
	if isolation != "" && !validIsolation(isolation) {
		return "", fmt.Errorf("不支持的隔离级别: %s", isolation)
	}
	conn, err := a.connection(connID)
	if err != nil {
		return "", err
	}
	if database == "" {
		database = conn.Database
	}
	s := a.sessionOf(tabID)
	s.BindConnection(connID)
	opener := txOpener{app: a, conn: conn, owner: s, isolation: isolation}
	timeout, warnAhead := a.txTimeouts()
	if err := s.Begin(opener, database, timeout, warnAhead); err != nil {
		return "", err
	}
	return s.Describe(), nil
}

// BeginTransactionDetail opens the tab's transaction and immediately reads the
// structured status back. The frontend used to issue these as two calls in one
// try block: a Begin that succeeded followed by a status read that failed (the
// concurrency gate's 503, a hiccup) left the transaction open on the server
// with no badge in the UI — the 30s calibrator only re-syncs tabs that already
// carry a badge, so it stayed invisible until the sweeper rolled it back and
// the user's edits vanished. One round trip makes the pair atomic; the detail
// read here cannot fail (same process, same session the Begin just wrote).
func (a *App) BeginTransactionDetail(connID, tabID, database, isolation string) (string, TxStatusDetail, error) {
	status, err := a.BeginTransactionIsolated(connID, tabID, database, isolation)
	if err != nil {
		return "", TxStatusDetail{}, err
	}
	return status, a.TransactionStatusDetail(tabID), nil
}

// txTimeouts resolves the configured absolute deadline and warn-ahead window
// into durations; both are 0 when governance is off (the setting's 0).
func (a *App) txTimeouts() (time.Duration, time.Duration) {
	st := a.GetSettings()
	if st.TxTimeoutMinutes <= 0 {
		return 0, 0
	}
	timeout := time.Duration(st.TxTimeoutMinutes) * time.Minute
	warnAhead := time.Duration(st.TxWarnAheadMinutes) * time.Minute
	return timeout, warnAhead
}

// CommitTransaction finalizes the tab's transaction.
func (a *App) CommitTransaction(connID, tabID string) (string, error) {
	started := time.Now()
	conn, err := a.connection(connID)
	if err != nil {
		a.recordTx("commit", started, err)
		return "", err
	}
	s := a.sessionOf(tabID)
	if err := s.Commit(txOpener{app: a, conn: conn, owner: s}); err != nil {
		a.recordTx("commit", started, err)
		return "", err
	}
	a.recordTx("commit", started, nil)
	return s.Describe(), nil
}

// RollbackTransaction discards the tab's transaction.
func (a *App) RollbackTransaction(connID, tabID string) (string, error) {
	started := time.Now()
	conn, err := a.connection(connID)
	if err != nil {
		a.recordTx("rollback", started, err)
		return "", err
	}
	s := a.sessionOf(tabID)
	if err := s.Rollback(txOpener{app: a, conn: conn, owner: s}); err != nil {
		a.recordTx("rollback", started, err)
		return "", err
	}
	a.recordTx("rollback", started, nil)
	return s.Describe(), nil
}

// TransactionStatus reports the tab's transaction, so the UI can show it.
func (a *App) TransactionStatus(tabID string) string {
	return a.sessionOf(tabID).Describe()
}

// TxStatusDetail is /api/tx-status's structured answer: the one-line status
// the editor shows, plus the deadline fields the UI turns into a countdown
// and a warning style (DESIGN-txn-timeout-2026-11-03 §6 phase 1). remaining
// is 0 and warned false when the session has no deadline (governance off or
// no transaction open); note is non-empty only after an automatic rollback.
type TxStatusDetail struct {
	Status    string `json:"status"`
	Remaining int64  `json:"remainingMs"` // ms until the deadline; 0 = no deadline
	Warned    bool   `json:"warned"`
	Note      string `json:"note,omitempty"`
}

// TransactionStatusDetail is the structured read the frontend polls.
func (a *App) TransactionStatusDetail(tabID string) TxStatusDetail {
	s := a.sessionOf(tabID)
	now := time.Now()
	remaining := s.Remaining(now)
	detail := TxStatusDetail{Status: s.Describe(), Warned: s.Warned(now), Note: s.Note()}
	if remaining > 0 {
		detail.Remaining = remaining.Milliseconds()
	}
	return detail
}

// sweepExpiredTransactions walks every tab's session and rolls back the ones
// past their absolute deadline. Called from the app's sweeper goroutine and,
// opportunistically, from the tx-status poll the frontend already runs —
// whichever fires first converges the session, and Sweep's lock discipline
// makes the double arrival safe (exactly one acts).
//
// HasTx (open or aborted) is the design's §7.3 ruling: an aborted transaction
// holds server-side resources just the same, and rollback is the only way out.
func (a *App) sweepExpiredTransactions() {
	type victim struct {
		tabID string
		sess  *txn.Session
		conn  *Connection
	}
	var due []victim
	now := time.Now()
	// Both maps are snapshotted under their own locks: txSessionsMu for the
	// session set, connectionsMu (read) for the connections the sweeps need —
	// the same discipline a.connection() enforces everywhere else. Shutdown's
	// write lock is what this read lock guards against.
	a.txSessionsMu.Lock()
	a.connectionsMu.RLock()
	for tabID, s := range a.txSessions {
		if s.TimedOut(now) {
			due = append(due, victim{tabID: tabID, sess: s, conn: a.connections[s.ConnectionID()]})
		}
	}
	a.connectionsMu.RUnlock()
	a.txSessionsMu.Unlock()

	for _, v := range due {
		if v.conn == nil {
			continue // the connection is gone; forgetSession owns that cleanup
		}
		acted, err := v.sess.Sweep(txOpener{app: a, conn: v.conn, owner: v.sess}, now)
		if err != nil {
			log.Printf("tx sweep %s: rollback failed: %v", v.tabID, err)
			continue
		}
		if acted {
			log.Printf("tx sweep: rolled back %s past its deadline", v.tabID)
			// The deadline already passed, so the session has nothing left to
			// do: drop its registration as well. Keeping it only grew
			// txSessions for tabs that are never closed (TP-06).
			a.forgetSession(v.tabID)
		}
	}
}

// startTxSweeper launches the periodic sweep (design §6 phase 2). The tick is
// coarse (10s) on purpose: a deadline is minutes long, and the frontend's
// tx-status poll sweeps opportunistically anyway, so this only catches the
// case where the window is closed or asleep.
func (a *App) startTxSweeper() {
	a.txSweeperMu.Lock()
	stop := make(chan struct{})
	a.txSweeperStop = stop
	a.txSweeperMu.Unlock()
	// The goroutine selects on the local stop, not on a.txSweeperStop: reading
	// the field back would race with stopTxSweeper nilling it under the mutex.
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				a.sweepExpiredTransactions()
			case <-stop:
				return
			}
		}
	}()
}

func (a *App) stopTxSweeper() {
	a.txSweeperMu.Lock()
	defer a.txSweeperMu.Unlock()
	if a.txSweeperStop != nil {
		close(a.txSweeperStop)
		a.txSweeperStop = nil
	}
}
