package main

// W14: 达梦 DM8.
//
// DM used to be recognized-but-refused here. It is now a real engine: the client
// opens it through dbcore/dm, which drives DM8 over its own wire protocol with the
// Apache-2.0 driver. These tests therefore assert the opposite of the earlier ones
// — the engine must reach the DM driver, and the failure of a closed port must come
// from THAT driver (a DM protocol error), not from a client-side refusal. That
// distinction is the whole regression: "unsupported database type: dm" would mean
// the wiring silently fell back.
//
// No DM server is available in this environment, so nothing here proves DM accepts
// a statement. The live checklist is dbcore/dm/dm_live_test.go, gated on
// DBCORE_TEST_DM_DSN.

import (
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
)

// dmTestConn builds a DM connection pointing at a closed port: connecting must fail,
// and the error must be the driver's.
func dmTestConn(t *testing.T, engine string) (*App, string) {
	t.Helper()
	app, _ := importApp(t)
	if err := app.AddConnection(ConnectionConfig{
		Name: "dm-test", Type: engine,
		Host: "127.0.0.1", Port: 5236,
		Username: "SYSDBA", Password: "SYSDBA", Database: "SYSDBA",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	conns := app.GetConnections()
	if len(conns) != 1 {
		t.Fatalf("conns = %+v", conns)
	}
	return app, conns[0].ID
}

func TestDMEngineResolvesToTheDMDialect(t *testing.T) {
	// The wiring, without a socket: every DM spelling must map to dbcore.KindDM, or
	// the connection would be handed to a different engine's DSN builder.
	for _, engine := range []string{"dm", "dameng"} {
		spec := connSpecAt(ConnectionConfig{Type: engine, Host: "h", Port: 5236}, "h", 5236)
		if spec.Kind != dbcore.KindDM {
			t.Fatalf("%s => kind %q, want %q", engine, spec.Kind, dbcore.KindDM)
		}
	}
	// And the alias table does not send it somewhere else.
	if got := builtinEngine("dm8"); got != "dm8" {
		// dm8 is not aliased to another engine: it is DM itself.
		t.Fatalf("builtinEngine(dm8) = %q", got)
	}
}

func TestDMConnectReachesTheDMDriver(t *testing.T) {
	// The regression this file exists for: connecting must fail inside the DM
	// driver (protocol/handshake), never in the client with "unsupported database
	// type". A closed port is the cheapest way to force that failure.
	app, id := dmTestConn(t, "dm")
	err := app.Connect(id)
	if err == nil {
		t.Fatal("a closed port cannot connect")
	}
	msg := err.Error()
	if strings.Contains(msg, "unsupported database type") {
		t.Fatalf("DM fell through to the client's default branch: %q", msg)
	}
	if strings.Contains(msg, "不是 Oracle") || strings.Contains(msg, "没有可用的驱动") {
		t.Fatalf("the old refusal is still in the path: %q", msg)
	}
	// The vendored driver tags its errors with DM's own codes ("Error -6001",
	// "-2501", …) and its own Chinese messages. Either shape proves the DM driver
	// answered: a closed port gives a network error, a live server with the wrong
	// password gives -2501. What must NOT appear is the client's own refusal.
	if !strings.Contains(msg, "Error -") && !strings.Contains(msg, "网络通信") &&
		!strings.Contains(msg, "dial address") && !strings.Contains(msg, "用户名或密码") {
		t.Fatalf("the error does not look like the DM driver's: %q", msg)
	}
}

func TestDMAliasConnectsThroughTheSameDriver(t *testing.T) {
	// "dameng" is the spelling the importer and the dialog may persist; it must
	// reach the same driver rather than a second code path.
	app, id := dmTestConn(t, "dameng")
	err := app.Connect(id)
	if err == nil {
		t.Fatal("a closed port cannot connect")
	}
	if strings.Contains(err.Error(), "unsupported database type") {
		t.Fatalf("the alias fell through: %v", err)
	}
}

func TestDMConnectionCanBeSavedAndListed(t *testing.T) {
	// The definition round-trips like any other engine's: a user can prepare a DM
	// connection (or import one) before a server exists.
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
	app2, _ := importApp(t)
	if err := app2.AddConnection(conns[0]); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if got := app2.GetConnections(); len(got) != 1 || got[0].Host != "dm.local" {
		t.Fatalf("round trip lost the definition: %+v", got)
	}
}

func TestDBeaverDMConnectionImportsAsAREalEngine(t *testing.T) {
	// The realistic migration path: a DBeaver profile with a 达梦 connection. The
	// import must carry it as a DM connection (type "dm"), so the sidebar shows a
	// working definition rather than dropping it.
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
	if conns[0].Port != 5236 {
		t.Fatalf("port = %d", conns[0].Port)
	}
	// No password was imported, so connecting fails on credentials or the network —
	// never on the engine being unknown.
	err = app.Connect(conns[0].ID)
	if err != nil && strings.Contains(err.Error(), "unsupported database type") {
		t.Fatalf("the imported DM connection is not routed to the DM driver: %v", err)
	}
}

func TestUnknownEngineStillSaysSoClearly(t *testing.T) {
	// The refusal mechanism that DM used to use is still there for an engine nobody
	// can open; an unknown spelling must not reach a driver.
	app, _ := importApp(t)
	if err := app.AddConnection(ConnectionConfig{
		Name: "nope", Type: "mystery-db", Host: "h", Port: 1,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	conns := app.GetConnections()
	err := app.Connect(conns[0].ID)
	if err == nil {
		t.Fatal("an unknown engine cannot connect")
	}
	if !strings.Contains(err.Error(), "unsupported database type") {
		t.Fatalf("the error should name the unsupported type: %v", err)
	}
}
