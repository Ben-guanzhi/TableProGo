package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// newTransferApp builds an App with two real SQLite connections: a source with a
// parent/child pair the copy has to order, and an empty target file.
//
// SQLite is the honest test bed here: it is a real driver through the kernel's
// BrowseRows/Begin/ExecuteIn path, so the orchestrator's ordering, batching,
// transactions and reconciliation all run against an engine rather than a stub.
func newTransferApp(t *testing.T) (*App, TransferTarget, TransferTarget) {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	dstPath := filepath.Join(dir, "dst.db")

	app := &App{
		connections: map[string]*Connection{},
		savedConnections: []ConnectionConfig{
			{ID: "src", Name: "src", Type: "sqlite", Database: srcPath},
			{ID: "dst", Name: "dst", Type: "sqlite", Database: dstPath},
		},
		settings: AppSettings{SafeMode: "off", ResultLimit: 1000},
	}
	for _, id := range []string{"src", "dst"} {
		if err := app.Connect(id); err != nil {
			t.Fatalf("connect %s: %v", id, err)
		}
	}
	t.Cleanup(func() {
		_ = app.Disconnect("src")
		_ = app.Disconnect("dst")
	})

	// The child table is created FIRST and declared before its parent, so a copy
	// that follows catalog order rather than the FK graph fails on the insert.
	for _, q := range []string{
		`CREATE TABLE orders(id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL,
			amount TEXT, FOREIGN KEY (customer_id) REFERENCES customers(id))`,
		`CREATE TABLE customers(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO customers(id,name) VALUES (1,'alice'),(2,'bob'),(3,'carol')`,
		`INSERT INTO orders(id,customer_id,amount) VALUES
			(1,1,'10.00'),(2,1,'20.50'),(3,2,'7.25')`,
	} {
		if r := app.ExecuteQuery("src", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}
	return app, TransferTarget{ConnID: "src"}, TransferTarget{ConnID: "dst"}
}

func TestTransferPreviewOrdersByForeignKeyNotCatalogOrder(t *testing.T) {
	app, src, dst := newTransferApp(t)
	preview, err := app.PreviewTransfer(TransferRequest{Source: src, Target: dst})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Tables) != 2 {
		t.Fatalf("tables = %+v", preview.Tables)
	}
	// customers must come before orders despite being created second.
	if preview.Tables[0].Source != "customers" {
		t.Fatalf("copy order = %+v, want customers first", preview.Tables)
	}
	if preview.Tables[0].Rows != 3 || preview.Tables[1].Rows != 3 {
		t.Fatalf("row counts = %+v", preview.Tables)
	}
	if preview.TotalRows != 6 {
		t.Fatalf("total rows = %d, want 6", preview.TotalRows)
	}
	// The preview must not have touched the target.
	if r := app.ExecuteQuery("dst", "SELECT 1", ""); r.Error != "" {
		t.Fatalf("target query: %s", r.Error)
	}
}

func TestTransferPreviewCarriesDDLWithoutRunningIt(t *testing.T) {
	app, src, dst := newTransferApp(t)
	preview, err := app.PreviewTransfer(TransferRequest{Source: src, Target: dst})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Statements) == 0 {
		t.Fatal("the preview must show the DDL it would run")
	}
	for _, s := range preview.Statements {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s.SQL)), "CREATE") {
			t.Fatalf("statement for %s is not DDL: %q", s.Table, s.SQL)
		}
	}
	// Nothing was created: a second preview sees the same "target missing".
	if _, err := app.PreviewTransfer(TransferRequest{Source: src, Target: dst}); err != nil {
		t.Fatalf("second preview: %v", err)
	}
}

func TestTransferApplyCopiesEverythingAndReconciles(t *testing.T) {
	app, src, dst := newTransferApp(t)
	res, err := app.ApplyTransfer(TransferRequest{Source: src, Target: dst, Confirm: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Aborted {
		t.Fatalf("copy aborted: %s", res.Error)
	}
	if res.Rows != 6 {
		t.Fatalf("rows written = %d, want 6", res.Rows)
	}
	// Every table's read/write counts must agree.
	for _, tr := range res.Tables {
		if tr.RowsRead != tr.RowsWritten {
			t.Fatalf("%s: read %d wrote %d", tr.Source, tr.RowsRead, tr.RowsWritten)
		}
		if tr.Error != "" {
			t.Fatalf("%s: %s", tr.Source, tr.Error)
		}
	}
	// The data really arrived, with the FK relationship intact.
	got := app.ExecuteQuery("dst", "SELECT count(*) AS n FROM orders", "")
	if got.Error != "" {
		t.Fatalf("query target: %s", got.Error)
	}
	if got.RowCount != 1 {
		t.Fatalf("orders rows = %d", got.RowCount)
	}
	joined := app.ExecuteQuery("dst",
		"SELECT c.name FROM orders o JOIN customers c ON c.id = o.customer_id ORDER BY o.id", "")
	if joined.Error != "" {
		t.Fatalf("join on target: %s", joined.Error)
	}
	if joined.RowCount != 3 {
		t.Fatalf("joined rows = %d, want 3", joined.RowCount)
	}
}

func TestTransferApplyRequiresConfirm(t *testing.T) {
	app, src, dst := newTransferApp(t)
	if _, err := app.ApplyTransfer(TransferRequest{Source: src, Target: dst}); err == nil {
		t.Fatal("an unconfirmed transfer must be refused")
	}
	// Nothing was created by the refused call.
	res := app.ExecuteQuery("dst", "SELECT count(*) FROM orders", "")
	if res.Error == "" {
		t.Fatalf("the target should still be empty, got %+v", res)
	}
}

func TestTransferApplyIsIdempotentAboutTableSelection(t *testing.T) {
	app, src, dst := newTransferApp(t)
	// Copying one table only.
	res, err := app.ApplyTransfer(TransferRequest{
		Source: src, Target: dst, Tables: []string{"customers"}, Confirm: true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Aborted {
		t.Fatalf("aborted: %s", res.Error)
	}
	if res.Rows != 3 {
		t.Fatalf("rows = %d, want 3 (customers only)", res.Rows)
	}
	// The FK target is missing for orders, so the copy of orders would have failed
	// had it been included; selecting one table must not drag the other in.
	if r := app.ExecuteQuery("dst", "SELECT count(*) FROM orders", ""); r.Error == "" {
		t.Fatal("orders must not have been created")
	}
}

func TestTransferRejectsUnknownTable(t *testing.T) {
	app, src, dst := newTransferApp(t)
	_, err := app.PreviewTransfer(TransferRequest{
		Source: src, Target: dst, Tables: []string{"nope"},
	})
	if err == nil {
		t.Fatal("an unknown table must be refused")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error should name the table: %v", err)
	}
}

func TestTransferSkipDataCreatesStructureOnly(t *testing.T) {
	app, src, dst := newTransferApp(t)
	res, err := app.ApplyTransfer(TransferRequest{
		Source: src, Target: dst, SkipData: true, Confirm: true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Aborted {
		t.Fatalf("aborted: %s", res.Error)
	}
	if res.Rows != 0 {
		t.Fatalf("rows = %d, want 0 with SkipData", res.Rows)
	}
	// The tables exist but are empty.
	if r := app.ExecuteQuery("dst", "SELECT count(*) FROM customers", ""); r.Error != "" {
		t.Fatalf("customers should exist: %s", r.Error)
	}
}

func TestTransferRoundTripsLargeTable(t *testing.T) {
	// More than one page, so the batching loop is exercised rather than the
	// single-page shortcut.
	app, src, dst := newTransferApp(t)
	for _, q := range []string{
		`CREATE TABLE big(id INTEGER PRIMARY KEY, payload TEXT)`,
		`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 1200)
		 INSERT INTO big(id,payload) SELECT n, 'row-' || n FROM seq`,
	} {
		if r := app.ExecuteQuery("src", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}
	res, err := app.ApplyTransfer(TransferRequest{
		Source: src, Target: dst, Tables: []string{"big"}, Confirm: true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Aborted {
		t.Fatalf("aborted: %s", res.Error)
	}
	if res.Rows != 1200 {
		t.Fatalf("rows = %d, want 1200 (three pages)", res.Rows)
	}
	// Spot-check a row from the last page: a batching bug typically loses the
	// tail.
	r := app.ExecuteQuery("dst", "SELECT payload FROM big WHERE id = 1200", "")
	if r.Error != "" {
		t.Fatalf("query: %s", r.Error)
	}
	if r.RowCount != 1 {
		t.Fatalf("the last row is missing: %d rows", r.RowCount)
	}
}

func TestTransferCycleDegradesHonestlyOnEngineWithoutAlterFK(t *testing.T) {
	// Two tables referencing each other need their constraints added after the
	// data. SQLite has no ALTER TABLE ... ADD CONSTRAINT, so the honest outcome
	// is: copy everything, skip the cyclic constraint, and SAY SO. Emitting the
	// ALTER anyway would fail the run; dropping it silently would hide it.
	app, src, dst := newTransferApp(t)
	for _, q := range []string{
		// Both directions declared, so the catalog really reports a cycle. SQLite
		// accepts the forward reference at CREATE time (constraints are not
		// enforced until the rows exist), which is exactly how a cyclic schema
		// comes to exist in the first place.
		`CREATE TABLE a(id INTEGER PRIMARY KEY, b_id INTEGER REFERENCES b(id))`,
		`CREATE TABLE b(id INTEGER PRIMARY KEY, a_id INTEGER REFERENCES a(id))`,
		`INSERT INTO a(id,b_id) VALUES (1,NULL)`,
		`INSERT INTO b(id,a_id) VALUES (1,1)`,
	} {
		if r := app.ExecuteQuery("src", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}
	preview, err := app.PreviewTransfer(TransferRequest{
		Source: src, Target: dst, Tables: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Tables) != 2 {
		t.Fatalf("tables = %+v", preview.Tables)
	}
	// The deferred constraint must be announced, not attempted.
	var announced bool
	for _, w := range preview.Warnings {
		if strings.Contains(w, "不支持事后添加外键") {
			announced = true
		}
	}
	if !announced {
		t.Fatalf("warnings = %v, want one explaining the skipped constraint", preview.Warnings)
	}

	// And the run still succeeds: data copies, only the constraint is absent.
	res, err := app.ApplyTransfer(TransferRequest{
		Source: src, Target: dst, Tables: []string{"a", "b"}, Confirm: true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Aborted {
		t.Fatalf("the copy should succeed: %s", res.Error)
	}
	if res.Rows != 2 {
		t.Fatalf("rows = %d, want 2", res.Rows)
	}
}

func TestTransferSelfReferenceCopiesItsRows(t *testing.T) {
	// A self-referencing table (emp.manager_id → emp.id) is a cycle of one. Its
	// rows must still arrive; on an engine that cannot add the constraint late,
	// the constraint is what gets dropped, never the data.
	app, src, dst := newTransferApp(t)
	for _, q := range []string{
		`CREATE TABLE emp(id INTEGER PRIMARY KEY, manager_id INTEGER REFERENCES emp(id), name TEXT)`,
		`INSERT INTO emp(id,manager_id,name) VALUES (1,NULL,'boss'),(2,1,'worker')`,
	} {
		if r := app.ExecuteQuery("src", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}
	res, err := app.ApplyTransfer(TransferRequest{
		Source: src, Target: dst, Tables: []string{"emp"}, Confirm: true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Aborted {
		t.Fatalf("the copy should succeed: %s", res.Error)
	}
	if res.Rows != 2 {
		t.Fatalf("rows = %d, want 2", res.Rows)
	}
}
