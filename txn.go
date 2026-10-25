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
	"strings"
	"sync"

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
	m  map[string]*dbcore.TxManager
}{m: map[string]*dbcore.TxManager{}}

func txKey(connID, database string) string { return connID + "|" + database }

// txManagerFor returns (and lazily starts) the manager for one connection's
// database. Callers must hold no locks: building the driver can open a pool.
//
// An empty database means "the connection's own", which is what the statement paths
// pass when the tab's picker is untouched. It is resolved here so Begin and later
// statements address the same registry: keying on the raw name would build a second
// manager and lose the transaction ("transaction not found").
func (a *App) txManagerFor(conn *Connection, database string) (*dbcore.TxManager, error) {
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
			// transaction goes away).
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
}

func (o txOpener) Begin(database string) (string, error) {
	m, err := o.app.txManagerFor(o.conn, database)
	if err != nil {
		return "", err
	}
	info, err := m.Begin(context.Background(), dbcore.TxMode{})
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

func (o txOpener) Commit(id string) error {
	m, err := o.app.txManagerFor(o.conn, o.owner.Database)
	if err != nil {
		return err
	}
	return m.Commit(id)
}

func (o txOpener) Rollback(id string) error {
	m, err := o.app.txManagerFor(o.conn, o.owner.Database)
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
	}
	return s
}

// forgetSession drops a tab's transaction state (the tab was closed). An open
// transaction is rolled back first: closing a tab must not leave one running.
func (a *App) forgetSession(tabID string) {
	a.txSessionsMu.Lock()
	s, ok := a.txSessions[tabID]
	delete(a.txSessions, tabID)
	a.txSessionsMu.Unlock()
	if !ok || s.Status == txn.None {
		return
	}
	conn, err := a.connection(s.ConnID)
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
// connection's own, matching how a statement without a picker resolves.
func (a *App) BeginTransaction(connID, tabID, database string) (string, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return "", err
	}
	if database == "" {
		database = conn.Database
	}
	s := a.sessionOf(tabID)
	s.ConnID = connID
	opener := txOpener{app: a, conn: conn, owner: s}
	if err := s.Begin(opener, database); err != nil {
		return "", err
	}
	return s.Describe(), nil
}

// CommitTransaction finalizes the tab's transaction.
func (a *App) CommitTransaction(connID, tabID string) (string, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return "", err
	}
	s := a.sessionOf(tabID)
	if err := s.Commit(txOpener{app: a, conn: conn, owner: s}); err != nil {
		return "", err
	}
	return s.Describe(), nil
}

// RollbackTransaction discards the tab's transaction.
func (a *App) RollbackTransaction(connID, tabID string) (string, error) {
	conn, err := a.connection(connID)
	if err != nil {
		return "", err
	}
	s := a.sessionOf(tabID)
	if err := s.Rollback(txOpener{app: a, conn: conn, owner: s}); err != nil {
		return "", err
	}
	return s.Describe(), nil
}

// TransactionStatus reports the tab's transaction, so the UI can show it.
func (a *App) TransactionStatus(tabID string) string {
	return a.sessionOf(tabID).Describe()
}
