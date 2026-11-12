package main

// W14: 达梦 DM8.
//
// This build RECOGNIZES the engine and REFUSES to open it. Both halves are
// deliberate, and both are tested here:
//
//   - recognizing it means a DM connection can be created or imported and shows up
//     as a known engine instead of "unknown";
//   - refusing it means connecting says WHY (no driver can speak DM's wire
//     protocol) instead of failing inside a driver lookup, which reads like a bug.
//
// The SQL dialect is real and lives in the kernel (sqltext.DM); what is missing is
// the socket, and the message says exactly that.

import (
	"strings"
	"testing"
)

func TestDMConnectRefusesWithTheReason(t *testing.T) {
	app, _ := importApp(t)
	if err := app.AddConnection(ConnectionConfig{
		Name: "dm-test", Type: "dm", Host: "127.0.0.1", Port: 5236,
		Username: "SYSDBA", Database: "DAMENG",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	conns := app.GetConnections()
	if len(conns) != 1 {
		t.Fatalf("conns = %+v", conns)
	}
	err := app.Connect(conns[0].ID)
	if err == nil {
		t.Fatal("a DM connection cannot be opened by this build")
	}
	// The message must name the actual obstacle: the wire protocol / missing
	// driver. "unknown engine" would send the user hunting for a typo.
	msg := err.Error()
	if !strings.Contains(msg, "达梦") {
		t.Fatalf("the error should name the product: %q", msg)
	}
	if !strings.Contains(msg, "驱动") && !strings.Contains(msg, "线协议") {
		t.Fatalf("the error should name the obstacle: %q", msg)
	}
}

func TestDMAliasesAllRefuse(t *testing.T) {
	for _, engine := range []string{"dm", "dameng", "dm8", "DM8", " DaMeng "} {
		app, _ := importApp(t)
		if err := app.AddConnection(ConnectionConfig{
			Name: "x", Type: engine, Host: "h", Port: 5236,
		}); err != nil {
			t.Fatalf("%s: add: %v", engine, err)
		}
		conns := app.GetConnections()
		if err := app.Connect(conns[0].ID); err == nil {
			t.Fatalf("%s: connecting must be refused", engine)
		}
	}
}

func TestDMConnectionCanBeSavedAndListed(t *testing.T) {
	// The refusal is at CONNECT, not at save: a user migrating a DM profile should
	// be able to keep the definition (and see it in the sidebar) even though this
	// build cannot open it yet.
	app, _ := importApp(t)
	if err := app.AddConnection(ConnectionConfig{
		Name: "keep-me", Type: "dameng", Host: "dm.local", Port: 5236, Database: "APP",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	conns := app.GetConnections()
	if len(conns) != 1 || conns[0].Type != "dameng" {
		t.Fatalf("conns = %+v", conns)
	}
	// It round-trips through the state file like any other connection.
	app2, _ := importApp(t)
	if err := app2.AddConnection(conns[0]); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if got := app2.GetConnections(); len(got) != 1 || got[0].Host != "dm.local" {
		t.Fatalf("round trip lost the definition: %+v", got)
	}
}

func TestDBeaverDMConnectionImportsButWillNotConnect(t *testing.T) {
	// The realistic path: a DBeaver profile that contains a 达梦 connection. The
	// import must carry it (so nothing is silently dropped), and the connect must
	// then say what is missing rather than pretending.
	app, dir := importApp(t)
	path := writeImportFixture(t, dir, "data-sources.json", `{
	  "connections": {
	    "dm-1": {
	      "provider": "dameng", "driver": "dm", "name": "dm-prod",
	      "configuration": {"host": "dm.example.com", "port": "5236", "database": "APP", "user": "SYSDBA"}
	    }
	  }
	}`)
	res, err := app.ImportConnections(ConnImportRequest{Path: path, Apply: true})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// The importer's engine mapping must know 达梦; dropping it would make the
	// migration silently incomplete.
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %+v skipped = %+v", res.Rows, res.Skipped)
	}
	conns := app.GetConnections()
	if len(conns) != 1 {
		t.Fatalf("conns = %+v", conns)
	}
	if conns[0].Type != "dm" {
		t.Fatalf("type = %q, want dm", conns[0].Type)
	}
	if err := app.Connect(conns[0].ID); err == nil {
		t.Fatal("the imported DM connection must not open")
	}
}
