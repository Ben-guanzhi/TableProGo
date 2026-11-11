package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// newRowSyncApp builds two real SQLite connections holding "the same table" with
// divergent rows: the source is the truth, the target is stale.
//
// SQLite keeps this honest — BrowseRows, ListIndexes and ExecuteIn all run
// against a real engine, so the ordering the merge depends on and the DML the
// apply issues are the production paths.
func newRowSyncApp(t *testing.T) (*App, TransferTarget, TransferTarget) {
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

	// Source: rows 1..5 with names a..e.
	// Target: row 1 (same), row 2 (stale name), row 4 (same), row 9 (extra).
	// The plan should therefore be: update 2, insert 3, insert 5, delete 9.
	setup := map[string][]string{
		"src": {
			`CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT NOT NULL, qty INTEGER)`,
			`INSERT INTO items(id,name,qty) VALUES (1,'a',10),(2,'b',20),(3,'c',30),(4,'d',40),(5,'e',50)`,
		},
		"dst": {
			`CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT NOT NULL, qty INTEGER)`,
			`INSERT INTO items(id,name,qty) VALUES (1,'a',10),(2,'STALE',20),(4,'d',40),(9,'gone',90)`,
		},
	}
	for id, stmts := range setup {
		for _, q := range stmts {
			if r := app.ExecuteQuery(id, q, ""); r.Error != "" {
				t.Fatalf("%s setup %q: %s", id, q, r.Error)
			}
		}
	}
	return app, TransferTarget{ConnID: "src"}, TransferTarget{ConnID: "dst"}
}

func TestRowSyncPreviewFindsEveryDifference(t *testing.T) {
	app, src, dst := newRowSyncApp(t)
	preview, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "items"})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Inserts != 2 {
		t.Fatalf("inserts = %d, want 2 (rows 3 and 5)", preview.Inserts)
	}
	if preview.Updates != 1 {
		t.Fatalf("updates = %d, want 1 (row 2)", preview.Updates)
	}
	if preview.Deletes != 1 {
		t.Fatalf("deletes = %d, want 1 (row 9)", preview.Deletes)
	}
	if preview.Total != 4 {
		t.Fatalf("total = %d, want 4", preview.Total)
	}
	if len(preview.PK) != 1 || !strings.EqualFold(preview.PK[0], "id") {
		t.Fatalf("pk = %v", preview.PK)
	}
	// Row 4 is identical on both sides and must not appear as a change.
	for _, c := range preview.Sample {
		if c.Key == "4" {
			t.Fatalf("row 4 is unchanged but produced %+v", c)
		}
	}
}

func TestRowSyncApplyConverges(t *testing.T) {
	app, src, dst := newRowSyncApp(t)
	preview, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "items"})
	if err != nil {
		t.Fatal(err)
	}
	seqs := make([]int, 0, len(preview.Sample))
	for _, c := range preview.Sample {
		seqs = append(seqs, c.Seq)
	}
	res, err := app.RowSyncApply(RowSyncApplyRequest{
		PlanID: preview.PlanID, Seqs: seqs, Confirm: true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("apply error: %s", res.Error)
	}
	if res.Applied != 4 {
		t.Fatalf("applied = %d, want 4", res.Applied)
	}

	// The tables now agree: a second analysis finds nothing.
	again, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "items"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Total != 0 {
		t.Fatalf("after convergence total = %d, want 0: %+v", again.Total, again.Sample)
	}

	// And the stale row is really gone from the target.
	r := app.ExecuteQuery("dst", "SELECT count(*) AS n FROM items WHERE id = 9", "")
	if r.Error != "" {
		t.Fatalf("query: %s", r.Error)
	}
	if r.RowCount != 1 {
		t.Fatalf("rows = %d", r.RowCount)
	}
}

func TestRowSyncApplyRequiresConfirm(t *testing.T) {
	app, src, dst := newRowSyncApp(t)
	preview, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "items"})
	if err != nil {
		t.Fatal(err)
	}
	seqs := []int{preview.Sample[0].Seq}
	if _, err := app.RowSyncApply(RowSyncApplyRequest{PlanID: preview.PlanID, Seqs: seqs}); err == nil {
		t.Fatal("an unconfirmed apply must be refused")
	}
}

func TestRowSyncApplyRefusesStalePlan(t *testing.T) {
	app, src, dst := newRowSyncApp(t)
	if _, err := app.RowSyncApply(RowSyncApplyRequest{
		PlanID: "rowsync-999", Seqs: []int{1}, Confirm: true,
	}); err == nil {
		t.Fatal("an unknown plan must be refused")
	}
	_ = src
	_ = dst
}

func TestRowSyncAppliesOnlyTickedChanges(t *testing.T) {
	app, src, dst := newRowSyncApp(t)
	preview, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "items"})
	if err != nil {
		t.Fatal(err)
	}
	// Tick only the deletion of row 9: the rest of the target must stay stale.
	var deleteSeq int
	for _, c := range preview.Sample {
		if c.Op == "delete" {
			deleteSeq = c.Seq
		}
	}
	if deleteSeq == 0 {
		t.Fatal("no delete in the plan")
	}
	res, err := app.RowSyncApply(RowSyncApplyRequest{
		PlanID: preview.PlanID, Seqs: []int{deleteSeq}, Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 1 || res.Deletes != 1 {
		t.Fatalf("result = %+v", res)
	}
	// Row 2 is still stale, so the tables have NOT converged.
	again, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "items"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Updates != 1 || again.Inserts != 2 {
		t.Fatalf("unticked changes should remain: %+v", again)
	}
}

func TestRowSyncRefusesTableWithoutPrimaryKey(t *testing.T) {
	app, src, dst := newRowSyncApp(t)
	for _, id := range []string{"src", "dst"} {
		if r := app.ExecuteQuery(id, "CREATE TABLE nopk(a TEXT, b TEXT)", ""); r.Error != "" {
			t.Fatalf("setup: %s", r.Error)
		}
	}
	_, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "nopk"})
	if err == nil {
		t.Fatal("a table without a primary key cannot be synced: there is no row identity")
	}
	if !strings.Contains(err.Error(), "主键") {
		t.Fatalf("error should name the missing PK: %v", err)
	}
}

func TestRowSyncRefusesMismatchedColumns(t *testing.T) {
	app, src, dst := newRowSyncApp(t)
	// The target grows a column the source lacks.
	if r := app.ExecuteQuery("dst", "ALTER TABLE items ADD COLUMN extra TEXT", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	_, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "items"})
	if err == nil {
		t.Fatal("a column mismatch must be refused, not silently dropped")
	}
	if !strings.Contains(err.Error(), "列集不一致") {
		t.Fatalf("error = %v", err)
	}
}

func TestRowSyncIgnoreColumnSuppressesUpdate(t *testing.T) {
	app, src, dst := newRowSyncApp(t)
	// A touched-at column differs on both sides for every row; ignoring it must
	// make the plan empty of updates for row 1 (whose other columns match).
	for _, id := range []string{"src", "dst"} {
		if r := app.ExecuteQuery(id, "ALTER TABLE items ADD COLUMN touched TEXT", ""); r.Error != "" {
			t.Fatalf("setup: %s", r.Error)
		}
	}
	if r := app.ExecuteQuery("src", "UPDATE items SET touched = '2026-01-01'", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	if r := app.ExecuteQuery("dst", "UPDATE items SET touched = '2026-06-01'", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}

	with := mustPreview(t, app, src, dst, nil)
	without := mustPreview(t, app, src, dst, []string{"touched"})
	// Rows 1 and 4 differ ONLY in the touched column, so ignoring it removes
	// exactly two updates; the stale row 2, the two inserts and the delete stay.
	if with.Total != without.Total+2 {
		t.Fatalf("ignoring touched: %d vs %d changes (want a difference of 2)", with.Total, without.Total)
	}
	if without.Total != 4 {
		t.Fatalf("without touched total = %d, want 4: %+v", without.Total, without.Sample)
	}
}

func mustPreview(t *testing.T, app *App, src, dst TransferTarget, ignore []string) RowSyncPreview {
	t.Helper()
	p, err := app.RowSyncPreview(RowSyncRequest{
		Source: src, Target: dst, Table: "items", IgnoreColumns: ignore,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return p
}

func TestRowSyncCompositePrimaryKey(t *testing.T) {
	app, src, dst := newRowSyncApp(t)
	for _, id := range []string{"src", "dst"} {
		for _, q := range []string{
			`CREATE TABLE pairs(a INTEGER NOT NULL, b INTEGER NOT NULL, v TEXT, PRIMARY KEY (a, b))`,
		} {
			if r := app.ExecuteQuery(id, q, ""); r.Error != "" {
				t.Fatalf("setup %q: %s", q, r.Error)
			}
		}
	}
	if r := app.ExecuteQuery("src", "INSERT INTO pairs(a,b,v) VALUES (1,1,'x'),(1,2,'y')", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	if r := app.ExecuteQuery("dst", "INSERT INTO pairs(a,b,v) VALUES (1,1,'x'),(2,1,'z')", ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	preview, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "pairs"})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.PK) != 2 {
		t.Fatalf("pk = %v, want the two PK columns", preview.PK)
	}
	// (1,1) matches; (1,2) is source-only; (2,1) is target-only.
	if preview.Inserts != 1 || preview.Deletes != 1 || preview.Updates != 0 {
		t.Fatalf("plan = %+v", preview)
	}
	seqs := make([]int, 0)
	for _, c := range preview.Sample {
		seqs = append(seqs, c.Seq)
	}
	if _, err := app.RowSyncApply(RowSyncApplyRequest{
		PlanID: preview.PlanID, Seqs: seqs, Confirm: true,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	again, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "pairs"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Total != 0 {
		t.Fatalf("composite-key convergence failed: %+v", again.Sample)
	}
}

func TestRowSyncLargeTableIsPaged(t *testing.T) {
	// More rows than one read page, so the fetcher's paging and the merge's
	// cross-batch boundary are both exercised.
	app, src, dst := newRowSyncApp(t)
	for _, id := range []string{"src", "dst"} {
		if r := app.ExecuteQuery(id, `CREATE TABLE big(id INTEGER PRIMARY KEY, v TEXT)`, ""); r.Error != "" {
			t.Fatalf("setup: %s", r.Error)
		}
	}
	if r := app.ExecuteQuery("src", `WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < 1200)
		INSERT INTO big(id,v) SELECT n, 'v' || n FROM s`, ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	// The target holds every even row: the source has all 1200, so the plan is
	// 600 inserts (the odd rows) and no deletes — nothing in the target is
	// missing from the source.
	if r := app.ExecuteQuery("dst", `WITH RECURSIVE s(n) AS (SELECT 2 UNION ALL SELECT n+2 FROM s WHERE n < 1200)
		INSERT INTO big(id,v) SELECT n, 'v' || n FROM s WHERE n % 2 = 0`, ""); r.Error != "" {
		t.Fatalf("setup: %s", r.Error)
	}
	preview, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "big"})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Inserts != 600 || preview.Deletes != 0 {
		t.Fatalf("plan = inserts %d deletes %d, want 600/0", preview.Inserts, preview.Deletes)
	}
	if preview.SourceRows != 1200 || preview.TargetRows != 600 {
		t.Fatalf("counts = src %d dst %d", preview.SourceRows, preview.TargetRows)
	}
	// The sample is bounded; the plan itself is not.
	if !preview.Truncated {
		t.Fatal("a 1200-change plan must report Truncated")
	}
	if len(preview.Sample) != maxRowSyncSample {
		t.Fatalf("sample = %d, want %d", len(preview.Sample), maxRowSyncSample)
	}
}
