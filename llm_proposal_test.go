package main

// The assistant's write path: the model proposes, the user disposes.
//
// The test that matters is the one that proves the proposal did NOT run by itself:
// the model's statement is in the database only after the user's accept.

import (
	"context"
	"testing"

	"tablepro-go/internal/llm"
)

func TestProposalIsNotRunUntilTheUserAccepts(t *testing.T) {
	conn := sqliteDB(t, "prop1", "scratch")
	createPartsTable(t, conn)
	a := searchApp(conn)

	// The model proposes a statement through the assistant's own tool.
	runner := a.llmRunner()
	allowProposals(t)

	out, err := runner(context.Background(), "propose_statement", map[string]interface{}{
		"connection": "prop1",
		"sql":        "INSERT INTO parts (id, name) VALUES (5, 'bolt')",
		"reason":     "the user asked for a bolt",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if out == "" {
		t.Fatal("the tool must tell the model the proposal is waiting")
	}

	// The table is still empty: the proposal did not run.
	if got := countParts(t, conn); got != 0 {
		t.Fatalf("the proposal ran by itself: %d rows", got)
	}

	// The proposal is waiting, and the user can see it.
	pending := a.PendingProposals()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	if pending[0].SQL == "" || pending[0].Connection != "prop1" {
		t.Fatalf("proposal = %+v", pending[0])
	}
	if pending[0].Reason == "" {
		t.Fatal("the model's reason must reach the user")
	}

	// The user accepts: now it runs.
	result, err := a.AcceptProposal(pending[0].ID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("accept: %s", result.Error)
	}
	if got := countParts(t, conn); got != 1 {
		t.Fatalf("after accept: %d rows", got)
	}
}

// Rejecting drops the proposal without running it.
func TestRejectedProposalDoesNotRun(t *testing.T) {
	conn := sqliteDB(t, "prop2", "scratch")
	createPartsTable(t, conn)
	a := searchApp(conn)
	allowProposals(t)

	if _, err := a.llmRunner()(context.Background(), "propose_statement", map[string]interface{}{
		"connection": "prop2",
		"sql":        "DELETE FROM parts",
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	pending := a.PendingProposals()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	if err := a.RejectProposal(pending[0].ID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got := countParts(t, conn); got != 0 {
		t.Fatalf("a rejected proposal ran: %d rows", got)
	}
	if len(a.PendingProposals()) != 0 {
		t.Fatal("a rejected proposal must not stay in the list")
	}
}

// A proposal is answered once: accepting it twice must not run it twice.
func TestProposalIsSingleUse(t *testing.T) {
	conn := sqliteDB(t, "prop3", "scratch")
	createPartsTable(t, conn)
	a := searchApp(conn)
	allowProposals(t)

	if _, err := a.llmRunner()(context.Background(), "propose_statement", map[string]interface{}{
		"connection": "prop3",
		"sql":        "INSERT INTO parts (id, name) VALUES (7, 'nut')",
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	id := a.PendingProposals()[0].ID
	if result, err := a.AcceptProposal(id); err != nil || result.Error != "" {
		t.Fatalf("accept: %v %v", err, result)
	}
	if _, err := a.AcceptProposal(id); err == nil {
		t.Fatal("a proposal must not be accepted twice")
	}
	if got := countParts(t, conn); got != 1 {
		t.Fatalf("the statement ran twice: %d rows", got)
	}
}

// Without the setting, the assistant has no write path at all: the tool is not
// offered, and calling it by hand is refused.
func TestProposalsAreOffByDefault(t *testing.T) {
	conn := sqliteDB(t, "prop4", "scratch")
	createPartsTable(t, conn)
	a := searchApp(conn)
	denyProposals(t)

	for _, tool := range a.llmTools() {
		if tool.Name == "propose_statement" {
			t.Fatal("the propose tool must not be offered when it is off")
		}
	}
	if _, err := a.llmRunner()(context.Background(), "propose_statement", map[string]interface{}{
		"connection": "prop4",
		"sql":        "DELETE FROM parts",
	}); err == nil {
		t.Fatal("the propose tool must be refused when it is off")
	}
	if got := countParts(t, conn); got != 0 {
		t.Fatalf("a refused proposal ran: %d rows", got)
	}
}

// The assistant's tool list carries the propose tool when it is on, so the model
// can actually find it.
func TestProposeToolIsAdvertisedWhenOn(t *testing.T) {
	a := searchApp()
	allowProposals(t)
	found := false
	for _, tool := range a.llmTools() {
		if tool.Name == "propose_statement" {
			found = true
			if tool.Description == "" {
				t.Error("the tool must explain itself")
			}
		}
	}
	if !found {
		t.Fatal("the propose tool must be offered when it is on")
	}
}

// The model proposing a statement must not make the assistant claim it ran.
func TestProposalTellsTheModelTheUserDecides(t *testing.T) {
	a := searchApp()
	allowProposals(t)
	out, err := a.runProposeTool(map[string]interface{}{
		"connection": "c1",
		"sql":        "UPDATE t SET x = 1",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !contains(out, "waiting for the user") {
		t.Fatalf("the tool must say the user decides: %q", out)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOfStr(haystack, needle) >= 0)
}

func indexOfStr(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// allowProposals turns the assistant's write path on for this test.
func allowProposals(t *testing.T) {
	t.Helper()
	cfg := LoadLLMConfig()
	cfg.AllowProposals = true
	if err := SaveLLMConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	t.Cleanup(func() {
		back := LoadLLMConfig()
		back.AllowProposals = false
		_ = SaveLLMConfig(back)
	})
}

// denyProposals turns it off.
func denyProposals(t *testing.T) {
	t.Helper()
	cfg := LoadLLMConfig()
	cfg.AllowProposals = false
	if err := SaveLLMConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// countParts counts the rows of the parts table.
func countParts(t *testing.T, conn *Connection) int {
	t.Helper()
	var count int
	if err := conn.DB.QueryRow(`SELECT count(*) FROM parts`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

// The proposal runs through the ordinary statement path, so Safe Mode still
// applies to it: a blocked statement is refused even after the user accepted it.
func TestAcceptedProposalStillObeysSafeMode(t *testing.T) {
	conn := sqliteDB(t, "prop5", "scratch")
	createPartsTable(t, conn)
	a := searchApp(conn)
	allowProposals(t)
	// blockNoWhere is the hard block: no confirmation can bypass it.
	settings := a.GetSettings()
	settings.SafeMode = "blockNoWhere"
	a.settings = settings

	if _, err := a.llmRunner()(context.Background(), "propose_statement", map[string]interface{}{
		"connection": "prop5",
		"sql":        "DELETE FROM parts",
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	id := a.PendingProposals()[0].ID
	result, err := a.AcceptProposal(id)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if result.Error == "" {
		t.Fatal("Safe Mode must still block an accepted proposal")
	}
	if got := countParts(t, conn); got != 0 {
		t.Fatalf("the blocked statement ran: %d rows", got)
	}
}

var _ = llm.Tool{}
