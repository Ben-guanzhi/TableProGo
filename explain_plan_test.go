package main

import (
	"strings"
	"testing"

	"tablepro-go/internal/explainplan"
)

// newExplainApp builds a SQLite app with a table worth planning a query over.
func newExplainApp(t *testing.T) *App {
	t.Helper()
	app, _ := newTestAppWithSQLite(t, safeModeOff)
	for _, q := range []string{
		`CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)`,
		`INSERT INTO items(id,name,qty) VALUES (1,'a',10),(2,'b',20),(3,'c',30)`,
	} {
		if r := app.ExecuteQuery("c1", q, ""); r.Error != "" {
			t.Fatalf("setup %q: %s", q, r.Error)
		}
	}
	return app
}

// flatten collects every node's label, so a test can assert the tree's contents
// without caring about its shape.
func flatten(n *explainplan.Node, out *[]string) {
	if n == nil {
		return
	}
	*out = append(*out, n.Label)
	for i := range n.Children {
		flatten(&n.Children[i], out)
	}
}

func TestExplainPlanTreeSQLiteWalksTheRows(t *testing.T) {
	// SQLite's EXPLAIN QUERY PLAN returns rows whose parent links ARE the tree,
	// so this is the one path that runs the full recipe against a real engine and
	// parses its real payload.
	app := newExplainApp(t)
	plan, err := app.ExplainPlanTree("c1", "SELECT * FROM items WHERE qty > 10", "")
	if err != nil {
		t.Fatalf("explain plan: %v", err)
	}
	if plan.Root == nil {
		t.Fatalf("no tree: %+v", plan)
	}
	if plan.Format != "rows" {
		t.Fatalf("format = %q, want rows", plan.Format)
	}
	var labels []string
	flatten(plan.Root, &labels)
	joined := strings.Join(labels, " | ")
	if !strings.Contains(strings.ToUpper(joined), "SCAN") {
		t.Fatalf("labels = %q, want a scan step", joined)
	}
	// The raw payload must be present so the "raw" view has something to show.
	if plan.Raw == "" {
		t.Fatal("raw payload is empty")
	}
}

func TestExplainPlanTreeSQLiteFlagsAFullScan(t *testing.T) {
	app := newExplainApp(t)
	// No index on qty: the plan is a full scan, which the parser flags.
	plan, err := app.ExplainPlanTree("c1", "SELECT * FROM items", "")
	if err != nil {
		t.Fatal(err)
	}
	var warns []string
	var walk func(n *explainplan.Node)
	walk = func(n *explainplan.Node) {
		if n == nil {
			return
		}
		if n.Warn != "" {
			warns = append(warns, n.Warn)
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	walk(plan.Root)
	if len(warns) == 0 {
		t.Fatalf("a full table scan should be flagged: %+v", plan.Root)
	}
}

func TestExplainPlanTreeEmptyStatementIsRefused(t *testing.T) {
	app := newExplainApp(t)
	if _, err := app.ExplainPlanTree("c1", "   ", ""); err == nil {
		t.Fatal("an empty statement has no plan")
	}
}

func TestExplainPlanTreeUnknownConnectionIsRefused(t *testing.T) {
	app := newExplainApp(t)
	if _, err := app.ExplainPlanTree("nope", "SELECT 1", ""); err == nil {
		t.Fatal("an unknown connection must be refused")
	}
}

// The text fallback must never claim a tree: Oracle only offers prose, and a
// renderer handed an empty tree would draw nothing while looking successful.
func TestTextPlanFallbackShape(t *testing.T) {
	p := explainplan.TextPlan("Plan hash value: 0")
	if p.Root != nil {
		t.Fatalf("text plan has a tree: %+v", p.Root)
	}
	if p.Format != "text" || p.Note == "" {
		t.Fatalf("plan = %+v", p)
	}
}
