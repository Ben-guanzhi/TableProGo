package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// importApp builds an App whose state files live in a temp dir, so an applied
// import never touches the developer's real connection list.
func importApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TABLEPRO_HOME", dir)
	app := &App{
		connections:      map[string]*Connection{},
		savedConnections: nil,
		settings:         AppSettings{SafeMode: "off", ResultLimit: 1000},
	}
	return app, dir
}

// writeImportFixture drops a fixture into the app's temp dir.
func writeImportFixture(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const dbeaverFixture = `{
  "connections": {
    "pg-1": {
      "provider": "postgresql", "name": "shop-prod", "save-password": true,
      "configuration": {"host": "db.example.com", "port": "5433", "database": "shop", "user": "alice",
        "url": "jdbc:postgresql://db.example.com:5433/shop?sslmode=require"}
    },
    "my-1": {
      "provider": "mysql", "name": "local", "save-password": false,
      "configuration": {"host": "127.0.0.1", "port": 3306, "database": "scratch", "user": "root"}
    },
    "sq-1": {
      "provider": "sqlite", "name": "notes", "configuration": {"database": "C:/data/notes.db"}
    },
    "mg-1": {"provider": "mongodb", "name": "docs", "configuration": {"host": "m.local"}}
  }
}`

func TestImportConnectionsPreviewsWithoutSaving(t *testing.T) {
	app, dir := importApp(t)
	path := writeImportFixture(t, dir, "data-sources.json", dbeaverFixture)

	res, err := app.ImportConnections(ConnImportRequest{Path: path})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	if res.Imported != 0 {
		t.Fatalf("a preview must not import: %d", res.Imported)
	}
	if len(app.savedConnections) != 0 {
		t.Fatal("a preview must not touch the saved list")
	}
	// The credentials note is present: the user learns it before applying.
	var told bool
	for _, n := range res.Notes {
		if strings.Contains(n, "口令") {
			told = true
		}
	}
	if !told {
		t.Fatalf("notes = %v", res.Notes)
	}
}

func TestImportConnectionsAppliesAndRenamesDuplicates(t *testing.T) {
	app, dir := importApp(t)
	// A connection that already carries one of the imported names.
	app.savedConnections = []ConnectionConfig{{ID: "existing", Name: "local", Type: "mysql"}}
	path := writeImportFixture(t, dir, "data-sources.json", dbeaverFixture)

	res, err := app.ImportConnections(ConnImportRequest{Path: path, Apply: true})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 3 {
		t.Fatalf("imported = %d, want 3", res.Imported)
	}
	if len(app.savedConnections) != 4 {
		t.Fatalf("saved = %d, want 4 (the existing one plus three)", len(app.savedConnections))
	}
	// The duplicate was renamed, not overwritten.
	var renamed bool
	for _, r := range res.Renamed {
		if strings.Contains(r, "local") && strings.Contains(r, "(2)") {
			renamed = true
		}
	}
	if !renamed {
		t.Fatalf("renamed = %v", res.Renamed)
	}
	// The original still exists untouched.
	var kept bool
	for _, c := range app.savedConnections {
		if c.ID == "existing" && c.Name == "local" {
			kept = true
		}
	}
	if !kept {
		t.Fatal("the pre-existing connection must survive the import")
	}
}

func TestImportConnectionsCarriesFieldsButNotCredentials(t *testing.T) {
	app, dir := importApp(t)
	path := writeImportFixture(t, dir, "data-sources.json", dbeaverFixture)
	if _, err := app.ImportConnections(ConnImportRequest{Path: path, Apply: true}); err != nil {
		t.Fatalf("import: %v", err)
	}
	var pg *ConnectionConfig
	for i := range app.savedConnections {
		if app.savedConnections[i].Name == "shop-prod" {
			pg = &app.savedConnections[i]
		}
	}
	if pg == nil {
		t.Fatalf("saved = %+v", app.savedConnections)
	}
	if pg.Type != "postgresql" || pg.Host != "db.example.com" || pg.Port != 5433 {
		t.Fatalf("pg = %+v", pg)
	}
	if pg.Username != "alice" || pg.Database != "shop" {
		t.Fatalf("pg = %+v", pg)
	}
	if pg.SSLMode != "require" {
		t.Fatalf("ssl = %q", pg.SSLMode)
	}
	// The one thing that must never arrive.
	if pg.Password != "" {
		t.Fatal("an imported connection must not carry a password")
	}

	// A SQLite row moves its file path into Database and leaves Host empty: that
	// is the shape the connection dialog and the driver expect.
	var sq *ConnectionConfig
	for i := range app.savedConnections {
		if app.savedConnections[i].Name == "notes" {
			sq = &app.savedConnections[i]
		}
	}
	if sq == nil {
		t.Fatal("the sqlite row is missing")
	}
	if sq.Type != "sqlite" || sq.Host != "" || !strings.HasSuffix(sq.Database, "notes.db") {
		t.Fatalf("sqlite = %+v", sq)
	}
}

func TestImportConnectionsFillsDefaultPort(t *testing.T) {
	app, dir := importApp(t)
	// DBeaver sometimes omits the port; the engine's default must fill in rather
	// than leaving 0 (which the dialog would show as an invalid connection).
	body := `{"connections":{"c1":{"provider":"mysql","name":"noport","configuration":{"host":"h"}}}}`
	path := writeImportFixture(t, dir, "data-sources.json", body)
	if _, err := app.ImportConnections(ConnImportRequest{Path: path, Apply: true}); err != nil {
		t.Fatal(err)
	}
	if len(app.savedConnections) != 1 || app.savedConnections[0].Port != 3306 {
		t.Fatalf("saved = %+v", app.savedConnections)
	}
}

func TestImportConnectionsRefusesNavicatNCXWithTheReason(t *testing.T) {
	// The honest boundary: .ncx is encrypted, so say so and name the readable
	// path instead of half-parsing a blob.
	app, dir := importApp(t)
	path := writeImportFixture(t, dir, "conn.ncx", "<?xml version=\"1.0\"?><Connections/>")
	_, err := app.ImportConnections(ConnImportRequest{Path: path})
	if err == nil {
		t.Fatal("an encrypted export must be refused")
	}
	if !strings.Contains(err.Error(), "加密") {
		t.Fatalf("error should name the encryption: %v", err)
	}
	if !strings.Contains(err.Error(), "URL") {
		t.Fatalf("error should name the readable path: %v", err)
	}
}

func TestImportConnectionsURLList(t *testing.T) {
	app, dir := importApp(t)
	path := writeImportFixture(t, dir, "conns.txt",
		"# my connections\npostgres://bob@pg.local:5432/app\nmysql://root@127.0.0.1:3306/x\n")
	res, err := app.ImportConnections(ConnImportRequest{Path: path, Apply: true})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 2 {
		t.Fatalf("imported = %d (%+v)", res.Imported, res.Skipped)
	}
	if app.savedConnections[0].Port != 5432 || app.savedConnections[1].Port != 3306 {
		t.Fatalf("saved = %+v", app.savedConnections)
	}
}

func TestImportConnectionsCSV(t *testing.T) {
	app, dir := importApp(t)
	path := writeImportFixture(t, dir, "conns.csv",
		"name,engine,host,port,user,database\n"+
			"a,postgresql,h1,5432,u1,d1\n"+
			"b,oracle,h2,1521,u2,d2\n")
	res, err := app.ImportConnections(ConnImportRequest{Path: path, Apply: true})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 2 {
		t.Fatalf("imported = %d", res.Imported)
	}
	if app.savedConnections[1].Type != "oracle" {
		t.Fatalf("saved = %+v", app.savedConnections)
	}
}

func TestImportConnectionsDetectsFormatByContent(t *testing.T) {
	// A renamed file must still be read correctly: the shape decides, not the
	// extension.
	app, dir := importApp(t)
	path := writeImportFixture(t, dir, "weird.dat", dbeaverFixture)
	if _, err := app.ImportConnections(ConnImportRequest{Path: path}); err != nil {
		t.Fatalf("content detection failed: %v", err)
	}
}

func TestImportConnectionsMissingFileIsAnError(t *testing.T) {
	app, _ := importApp(t)
	if _, err := app.ImportConnections(ConnImportRequest{Path: filepath.Join(t.TempDir(), "nope.json")}); err == nil {
		t.Fatal("a missing file must be an error")
	}
}

func TestImportConnectionsGroupIsApplied(t *testing.T) {
	app, dir := importApp(t)
	path := writeImportFixture(t, dir, "data-sources.json", dbeaverFixture)
	if _, err := app.ImportConnections(ConnImportRequest{Path: path, Apply: true, Group: " 迁移 "}); err != nil {
		t.Fatal(err)
	}
	for _, c := range app.savedConnections {
		if c.Group != "迁移" {
			t.Fatalf("group = %q, want the trimmed label", c.Group)
		}
	}
}

// TestImportConnectionsOverHTTP drives the real route, not just the method: the
// JSON shape the dialog sends (and the report it reads back) has to survive the
// wire, and a 400/500 split tells the UI whether to fix the request or the file.
func TestImportConnectionsOverHTTP(t *testing.T) {
	app, dir := importApp(t)
	path := writeImportFixture(t, dir, "data-sources.json", dbeaverFixture)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	app.startHTTPServerOnListener(listener)
	url := "http://" + listener.Addr().String() + "/api/connections/import"

	post := func(req ConnImportRequest) (int, []byte) {
		t.Helper()
		body, _ := json.Marshal(req)
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		payload, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, payload
	}

	// Preview: 200 with rows and no writes.
	code, payload := post(ConnImportRequest{Path: path})
	if code != 200 {
		t.Fatalf("preview status = %d: %s", code, payload)
	}
	var preview ConnImportResult
	if err := json.Unmarshal(payload, &preview); err != nil {
		t.Fatalf("decode preview: %v (%s)", err, payload)
	}
	if len(preview.Rows) != 3 || preview.Imported != 0 {
		t.Fatalf("preview = %+v", preview)
	}

	// Apply: the same route with apply=true saves.
	code, payload = post(ConnImportRequest{Path: path, Apply: true})
	if code != 200 {
		t.Fatalf("apply status = %d: %s", code, payload)
	}
	var applied ConnImportResult
	if err := json.Unmarshal(payload, &applied); err != nil {
		t.Fatalf("decode apply: %v", err)
	}
	if applied.Imported != 3 {
		t.Fatalf("applied = %+v", applied)
	}

	// A bad request (no path) is a 400, so the dialog can tell the user to pick
	// a file rather than showing a server error.
	if code, _ := post(ConnImportRequest{}); code != 400 {
		t.Fatalf("empty path status = %d, want 400", code)
	}
}
