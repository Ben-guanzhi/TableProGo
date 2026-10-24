//go:build !windows || !cgo_is_disabled

package main

// Live transaction proof: a statement run inside a tab's transaction is invisible
// to another connection until it commits, and a rollback takes it back.
//
// This is the test that keeps the feature honest. A unit test on internal/txn can
// only show the state machine; only a real server shows that BEGIN actually holds
// the rows.

import (
	"context"
	"os"
	"testing"
)

const connIDForTest = "tx-live"

func livePGDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TABULA_LIVE_PG")
	if dsn == "" {
		t.Skip("set TABULA_LIVE_PG to run the live transaction test")
	}
	return dsn
}

// setupTxApp connects to the live PostgreSQL from TABULA_LIVE_PG and returns an app
// plus a table to work on.
func setupTxApp(t *testing.T) (*App, *Connection) {
	t.Helper()
	raw := livePGDSN(t)
	cfg := connectionConfigFromURL(t, "postgresql", raw)
	db, err := createConnection(cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		// The registry is process-wide; drop this connection's managers before the
		// pool goes away, exactly as Disconnect does.
		closeTxns(connIDForTest)
		db.Close()
	})
	conn := &Connection{
		ID:       connIDForTest,
		Name:     "tx-live",
		Type:     "postgresql",
		Database: cfg.Database,
		Config:   cfg,
		DB:       db,
	}

	a := &App{connections: map[string]*Connection{conn.ID: conn}}
	if _, err := conn.DB.Exec(`DROP TABLE IF EXISTS tx_probe`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := conn.DB.Exec(`CREATE TABLE tx_probe (id integer primary key)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { conn.DB.Exec(`DROP TABLE IF EXISTS tx_probe`) })
	return a, conn
}

// countProbe reads the table through a pool of its own, which is how another client
// would see it.
func countProbe(t *testing.T, a *App, conn *Connection) int {
	t.Helper()
	drv, err := a.execDriver(conn, "")
	if err != nil {
		t.Fatalf("driver: %v", err)
	}
	res, err := drv.Execute(context.Background(), `SELECT count(*) FROM tx_probe`)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		t.Fatalf("no count row: %+v", res)
	}
	return int(res.Rows[0][0].I)
}

func TestTransactionHoldsRowsUntilCommit(t *testing.T) {
	a, conn := setupTxApp(t)

	if _, err := a.BeginTransaction(conn.ID, "tab1", ""); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if got := a.TransactionStatus("tab1"); got == "" {
		t.Fatal("the tab must report an open transaction")
	}

	result := a.ExecuteQueryInTab(conn.ID, "tab1", `INSERT INTO tx_probe (id) VALUES (1)`, "")
	if result.Error != "" {
		t.Fatalf("insert: %s", result.Error)
	}
	// Inside the transaction the row is there...
	if result := a.ExecuteQueryInTab(conn.ID, "tab1", `SELECT count(*) FROM tx_probe`, ""); result.Error != "" {
		t.Fatalf("select inside: %s", result.Error)
	} else if result.Rows[0][0] != int64(1) && result.Rows[0][0] != float64(1) {
		t.Fatalf("inside count = %v", result.Rows[0][0])
	}
	// ...and outside it is not, which is what "transaction" has to mean.
	if got := countProbe(t, a, conn); got != 0 {
		t.Fatalf("an uncommitted row was visible outside the transaction: %d", got)
	}

	if _, err := a.CommitTransaction(conn.ID, "tab1"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := countProbe(t, a, conn); got != 1 {
		t.Fatalf("after commit count = %d, want 1", got)
	}
	if got := a.TransactionStatus("tab1"); got != "" {
		t.Fatalf("after commit the tab must be in autocommit, got %q", got)
	}
}

func TestTransactionRollbackDiscardsRows(t *testing.T) {
	a, conn := setupTxApp(t)

	if _, err := a.BeginTransaction(conn.ID, "tab2", ""); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if result := a.ExecuteQueryInTab(conn.ID, "tab2", `INSERT INTO tx_probe (id) VALUES (7)`, ""); result.Error != "" {
		t.Fatalf("insert: %s", result.Error)
	}
	if _, err := a.RollbackTransaction(conn.ID, "tab2"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := countProbe(t, a, conn); got != 0 {
		t.Fatalf("a rolled-back row survived: %d", got)
	}
}

// Two tabs on one connection must not share a transaction: the whole point of
// keeping the session per tab.
func TestTwoTabsKeepSeparateTransactions(t *testing.T) {
	a, conn := setupTxApp(t)

	if _, err := a.BeginTransaction(conn.ID, "tabA", ""); err != nil {
		t.Fatalf("begin A: %v", err)
	}
	if result := a.ExecuteQueryInTab(conn.ID, "tabA", `INSERT INTO tx_probe (id) VALUES (10)`, ""); result.Error != "" {
		t.Fatalf("insert A: %s", result.Error)
	}
	// Tab B is in autocommit: its insert must be visible outside immediately.
	if result := a.ExecuteQueryInTab(conn.ID, "tabB", `INSERT INTO tx_probe (id) VALUES (20)`, ""); result.Error != "" {
		t.Fatalf("insert B: %s", result.Error)
	}
	if got := countProbe(t, a, conn); got != 1 {
		t.Fatalf("only tab B's row should be visible, got %d", got)
	}

	if _, err := a.RollbackTransaction(conn.ID, "tabA"); err != nil {
		t.Fatalf("rollback A: %v", err)
	}
	if got := countProbe(t, a, conn); got != 1 {
		t.Fatalf("tab B's row must survive tab A's rollback, got %d", got)
	}
}

// A statement that fails aborts the transaction: the next one is refused until the
// user rolls back.
func TestFailedStatementAbortsTheTransaction(t *testing.T) {
	a, conn := setupTxApp(t)

	if _, err := a.BeginTransaction(conn.ID, "tab3", ""); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if result := a.ExecuteQueryInTab(conn.ID, "tab3", `INSERT INTO tx_probe (id) VALUES (1)`, ""); result.Error != "" {
		t.Fatalf("first insert: %s", result.Error)
	}
	// A duplicate key aborts a PostgreSQL transaction.
	if result := a.ExecuteQueryInTab(conn.ID, "tab3", `INSERT INTO tx_probe (id) VALUES (1)`, ""); result.Error == "" {
		t.Fatal("the duplicate key must fail")
	}
	if result := a.ExecuteQueryInTab(conn.ID, "tab3", `INSERT INTO tx_probe (id) VALUES (2)`, ""); result.Error == "" {
		t.Fatal("a statement after the abort must be refused")
	}
	if _, err := a.RollbackTransaction(conn.ID, "tab3"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := countProbe(t, a, conn); got != 0 {
		t.Fatalf("nothing should have survived the aborted transaction, got %d", got)
	}
	// The tab works again in autocommit.
	if result := a.ExecuteQueryInTab(conn.ID, "tab3", `INSERT INTO tx_probe (id) VALUES (3)`, ""); result.Error != "" {
		t.Fatalf("autocommit after rollback: %s", result.Error)
	}
	if got := countProbe(t, a, conn); got != 1 {
		t.Fatalf("autocommit row missing, got %d", got)
	}
}
