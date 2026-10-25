package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// newCompareTestApp stands up one App holding two separate SQLite files, which
// is the smallest thing that can exercise a comparison: two live connections the
// plan has to read and then write to.
func newCompareTestApp(t *testing.T, mode string) *App {
	t.Helper()
	dir := t.TempDir()
	app := &App{
		connections: map[string]*Connection{},
		savedConnections: []ConnectionConfig{
			{ID: "src", Name: "源库", Type: "sqlite", Database: filepath.Join(dir, "src.db")},
			{ID: "dst", Name: "目标库", Type: "sqlite", Database: filepath.Join(dir, "dst.db")},
		},
		settings: AppSettings{SafeMode: mode, ResultLimit: 1000},
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
	return app
}

func execSetup(t *testing.T, app *App, connID string, statements ...string) {
	t.Helper()
	for _, q := range statements {
		if r := app.ExecuteQuery(connID, q, ""); r.Error != "" {
			t.Fatalf("setup %s %q: %s", connID, q, r.Error)
		}
	}
}

func seedCompareFixtures(t *testing.T, app *App) {
	execSetup(t, app, "src",
		`CREATE TABLE author (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE book (id INTEGER PRIMARY KEY, title TEXT)`,
		`CREATE INDEX idx_book_title ON book (title)`,
	)
	execSetup(t, app, "dst",
		`CREATE TABLE author (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE stale (id INTEGER PRIMARY KEY)`,
	)
}

func allEntryIDs(entries []CompareEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestCompareSchemasPlansThenApplyConverges(t *testing.T) {
	app := newCompareTestApp(t, "off")
	seedCompareFixtures(t, app)

	plan, err := app.CompareSchemas(
		CompareTarget{ConnID: "src"}, CompareTarget{ConnID: "dst"}, CompareOptions{})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if plan.PlanID == "" {
		t.Fatal("a plan must have an id, otherwise it cannot be applied")
	}
	if len(plan.Entries) == 0 {
		t.Fatal("two different files must produce a plan")
	}
	if plan.SourceLabel == "" || plan.TargetLabel == "" {
		t.Errorf("both sides must be labelled: %q -> %q", plan.SourceLabel, plan.TargetLabel)
	}

	// Destructive changes start unticked: running them takes a deliberate act.
	sawDestructive := false
	for _, e := range plan.Entries {
		if e.Destructive {
			sawDestructive = true
			if e.Selected {
				t.Errorf("destructive entry %s must not start selected", e.ID)
			}
		}
	}
	if !sawDestructive {
		t.Error("the fixture drops a table, so at least one entry must be destructive")
	}

	applied, err := app.ApplySync(ApplySyncRequest{PlanID: plan.PlanID, EntryIDs: allEntryIDs(plan.Entries), Confirm: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied.Failures) != 0 {
		t.Fatalf("failures = %+v", applied.Failures)
	}
	if applied.Applied != len(plan.Statements) {
		t.Fatalf("applied %d of %d statements", applied.Applied, len(plan.Statements))
	}

	// The point of the whole exercise: a second comparison of the same two sides
	// must now be empty.
	again, err := app.CompareSchemas(
		CompareTarget{ConnID: "src"}, CompareTarget{ConnID: "dst"}, CompareOptions{})
	if err != nil {
		t.Fatalf("re-compare: %v", err)
	}
	if len(again.Entries) != 0 {
		t.Fatalf("sync did not converge, remaining: %+v", again.Entries)
	}
}

func TestCompareSchemasRunsUnderTheStrictestSafeMode(t *testing.T) {
	// DDL has no row filter to lose, so Safe Mode's WHERE-oriented levels must
	// not stand between the user and a sync they confirmed.
	app := newCompareTestApp(t, "blockNoWhere")
	seedCompareFixtures(t, app)

	plan, err := app.CompareSchemas(
		CompareTarget{ConnID: "src"}, CompareTarget{ConnID: "dst"}, CompareOptions{})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	safe := make([]string, 0, len(plan.Entries))
	for _, e := range plan.Entries {
		if !e.Destructive {
			safe = append(safe, e.ID)
		}
	}
	applied, err := app.ApplySync(ApplySyncRequest{PlanID: plan.PlanID, EntryIDs: safe, Confirm: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied.Failures) != 0 || applied.Applied == 0 {
		t.Fatalf("applied = %+v", applied)
	}
}

func TestApplySyncRequiresAnExplicitConfirmation(t *testing.T) {
	app := newCompareTestApp(t, "off")
	seedCompareFixtures(t, app)
	plan, err := app.CompareSchemas(
		CompareTarget{ConnID: "src"}, CompareTarget{ConnID: "dst"}, CompareOptions{})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}

	if _, err := app.ApplySync(ApplySyncRequest{PlanID: plan.PlanID, EntryIDs: allEntryIDs(plan.Entries)}); err == nil {
		t.Fatal("applying without Confirm must be refused")
	} else if !strings.Contains(err.Error(), "确认") {
		t.Errorf("error = %v, want it to name the missing confirmation", err)
	}
}

func TestApplySyncRefusesAPlanItDidNotCompute(t *testing.T) {
	app := newCompareTestApp(t, "off")
	seedCompareFixtures(t, app)
	plan, err := app.CompareSchemas(
		CompareTarget{ConnID: "src"}, CompareTarget{ConnID: "dst"}, CompareOptions{})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if _, err := app.ApplySync(ApplySyncRequest{PlanID: "plan-forged", EntryIDs: allEntryIDs(plan.Entries), Confirm: true}); err == nil {
		t.Fatal("an unknown plan id must not run anything")
	}
	// Nothing ran: the plan is still fully outstanding.
	again, err := app.CompareSchemas(
		CompareTarget{ConnID: "src"}, CompareTarget{ConnID: "dst"}, CompareOptions{})
	if err != nil {
		t.Fatalf("re-compare: %v", err)
	}
	if len(again.Entries) != len(plan.Entries) {
		t.Errorf("entries changed after a refused apply: %d then %d", len(plan.Entries), len(again.Entries))
	}
}

func TestApplySyncSelectsOnlyWhatWasAskedFor(t *testing.T) {
	app := newCompareTestApp(t, "off")
	seedCompareFixtures(t, app)
	plan, err := app.CompareSchemas(
		CompareTarget{ConnID: "src"}, CompareTarget{ConnID: "dst"}, CompareOptions{})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}

	// Tick the single non-destructive change that adds a column, and nothing
	// else: the dropped table must still be there afterwards.
	var addColumn string
	for _, e := range plan.Entries {
		if e.Op == "add_column" {
			addColumn = e.ID
		}
	}
	if addColumn == "" {
		t.Fatalf("no add_column in the plan: %+v", plan.Entries)
	}
	applied, err := app.ApplySync(ApplySyncRequest{PlanID: plan.PlanID, EntryIDs: []string{addColumn}, Confirm: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Applied != 1 {
		t.Fatalf("applied = %d, want 1", applied.Applied)
	}

	if r := app.ExecuteQuery("dst", "SELECT name FROM author", ""); r.Error != "" {
		t.Fatalf("the added column is not there: %s", r.Error)
	}
	if r := app.ExecuteQuery("dst", "SELECT id FROM stale", ""); r.Error != "" {
		// The table is gone, so the drop ran even though it was not ticked.
		t.Errorf("an unticked drop must not have run: %s", r.Error)
	}
}
