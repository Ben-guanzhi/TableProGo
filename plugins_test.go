package main

// A plugin-backed connection, driven through the app the same way a built-in engine
// is. This is the test that proves the feature: the engine is not compiled in, the
// child process answers, and the catalog and query paths work unchanged.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/dbcore/dbcore/plugindriver"
)

// fakePluginMain is a plugin that claims the engine "fakeengine" and answers the
// connection-oriented calls this test makes. It is written to a temp module and
// built, so the launch path is the real one: the host starts the process, opens
// a connection, and binds it before the catalog and query calls arrive. The wire
// shapes mirror the deployed fynequery plugins (proto.CellPayload etc.), so the
// same decoders a real plugin relies on are exercised here.
const fakePluginMain = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    "fakeengine",
		Name:    "Fake Engine",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var req plugindriver.Request
			if json.Unmarshal(line, &req) == nil {
				send := func(result interface{}, errText, code string) {
					payload, _ := json.Marshal(result)
					frame, _ := json.Marshal(plugindriver.Response{ID: req.ID, Result: payload, Error: errText, Code: code})
					fmt.Fprintf(out, "%s\n", frame)
					out.Flush()
				}
				switch req.Method {
				case plugindriver.MethodPing:
					send("pong", "", "")
				case "open":
					send(plugindriver.OpenResult{ConnID: "c1"}, "", "")
				case "close":
					send(nil, "", "")
					os.Exit(0)
				case "tables":
					send([]plugindriver.ConnObject{
						{Name: "widgets", Kind: "table"},
						{Name: "recent", Kind: "view"},
					}, "", "")
				case "columns":
					send([]plugindriver.ConnColumn{
						{Name: "id", Type: "INTEGER", Key: "PK"},
						{Name: "label", Type: "TEXT", Nullable: true},
					}, "", "")
				case "browse":
					var params plugindriver.ConnBrowseParams
					_ = json.Unmarshal(req.Params, &params)
					send(plugindriver.ConnQueryResult{
						Columns: []plugindriver.ConnColumn{
							{Name: "id", Type: "INTEGER"},
							{Name: "label", Type: "TEXT"},
						},
						Rows: [][]interface{}{
							[]interface{}{plugindriver.CellPayload{Kind: "int", Int: 1}, plugindriver.CellPayload{Kind: "text", Text: fmt.Sprintf("first@%d", params.Limit)}},
							[]interface{}{plugindriver.CellPayload{Kind: "int", Int: 2}, plugindriver.CellPayload{Kind: "text", Text: "second"}},
						},
					}, "", "")
				case "query":
					send(plugindriver.ConnQueryResult{
						Columns: []plugindriver.ConnColumn{{Name: "answer", Type: "INTEGER"}},
						Rows:    [][]interface{}{[]interface{}{plugindriver.CellPayload{Kind: "int", Int: 42}}},
					}, "", "")
				default:
					send(nil, "unknown method "+req.Method, dbcore.ErrCodeNotSupported)
				}
			}
		}
		if err != nil {
			return
		}
	}
}
`

// buildFakeEnginePlugin builds the plugin and registers it for this test.
func buildFakeEnginePlugin(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fakePluginMain), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	goMod := "module fakeengine\n\ngo 1.21\n\nrequire github.com/dbcore/dbcore v0.0.0\n\nreplace github.com/dbcore/dbcore => " +
		filepath.ToSlash(filepath.Join(root, "dbcore")) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	binary := filepath.Join(dir, "fakeengine")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}
	plugins.register("fakeengine", binary)
	t.Cleanup(func() { plugins.unregister("fakeengine") })
}

// A plugin connection has no *sql.DB, so the whole catalog path has to work from the
// child process instead.
func TestPluginConnectionServesMetadata(t *testing.T) {
	buildFakeEnginePlugin(t)
	conn := &Connection{ID: "fake1", Name: "fake", Type: "fakeengine", Database: "main"}
	// The child holds the binary open; on Windows that would block the cleanup of
	// the temp directory, so it is stopped with the test.
	defer conn.closePlugin()
	a := searchApp(conn)

	schemas, err := a.GetDatabases(conn.ID)
	if err != nil {
		t.Fatalf("schemas: %v", err)
	}
	if len(schemas) == 0 || schemas[0] != "main" {
		t.Fatalf("schemas = %v", schemas)
	}

	tables, err := a.GetTables(conn.ID, "main", "")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(tables) != 2 || tables[0].Name != "widgets" || tables[1].Type != "view" {
		t.Fatalf("tables = %+v", tables)
	}

	columns, err := a.GetColumns(conn.ID, "main", "", "widgets")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if len(columns) != 2 || !columns[0].IsPrimaryKey {
		t.Fatalf("columns = %+v", columns)
	}
}

// A statement through a plugin connection reaches the child and its rows come back.
func TestPluginConnectionExecutesStatements(t *testing.T) {
	buildFakeEnginePlugin(t)
	conn := &Connection{ID: "fake2", Name: "fake", Type: "fakeengine", Database: "main"}
	defer conn.closePlugin()
	a := searchApp(conn)

	result := a.ExecuteQuery(conn.ID, "SELECT answer", "")
	if result.Error != "" {
		t.Fatalf("execute: %s", result.Error)
	}
	if result.RowCount != 1 {
		t.Fatalf("rows = %+v", result.Rows)
	}
	if value, ok := result.Rows[0][0].(int64); !ok || value != 42 {
		t.Fatalf("cell = %#v", result.Rows[0][0])
	}
}

// The data page of a plugin connection is rendered by the plugin, not by the
// client's own SQL builder.
func TestPluginConnectionServesDataPages(t *testing.T) {
	buildFakeEnginePlugin(t)
	conn := &Connection{ID: "fake5", Name: "fake", Type: "fakeengine", Database: "main"}
	defer conn.closePlugin()
	a := searchApp(conn)

	page := a.Preview(conn.ID, "main", "", "widgets", 10, 0, nil)
	if page.Error != "" {
		t.Fatalf("preview: %s", page.Error)
	}
	if page.RowCount != 2 {
		t.Fatalf("rows = %+v", page.Rows)
	}
	if len(page.Columns) != 2 || page.Columns[0].Name != "id" {
		t.Fatalf("columns = %+v", page.Columns)
	}
	if value, ok := page.Rows[1][1].(string); !ok || value != "second" {
		t.Fatalf("cell = %#v", page.Rows[1][1])
	}
}

// An unset preview request is normalized before it reaches a plugin: the child
// sees the default page size, not a raw 0 (the page contract previewPage states).
func TestPluginPreviewNormalizesUnsetLimit(t *testing.T) {
	buildFakeEnginePlugin(t)
	conn := &Connection{ID: "fake6", Name: "fake", Type: "fakeengine", Database: "main"}
	defer conn.closePlugin()
	a := searchApp(conn)

	page := a.Preview(conn.ID, "main", "", "widgets", 0, 0, nil)
	if page.Error != "" {
		t.Fatalf("preview: %s", page.Error)
	}
	value, ok := page.Rows[0][1].(string)
	if !ok || value != fmt.Sprintf("first@%d", defaultPreviewRows) {
		t.Fatalf("cell = %#v, want first@%d", page.Rows[0][1], defaultPreviewRows)
	}
}

// An engine with no plugin registered must say so, not fail somewhere deeper.
func TestUnknownEngineWithoutPlugin(t *testing.T) {
	conn := &Connection{ID: "none", Name: "none", Type: "not-an-engine"}
	a := searchApp(conn)
	_, err := a.GetTables(conn.ID, "x", "")
	if err == nil {
		t.Fatal("want an error for an engine with no driver and no plugin")
	}
	if !strings.Contains(err.Error(), "not-an-engine") {
		t.Fatalf("err = %v", err)
	}
}

// Disconnecting must take the child process down with it.
func TestPluginConnectionClosesOnDisconnect(t *testing.T) {
	buildFakeEnginePlugin(t)
	conn := &Connection{ID: "fake3", Name: "fake", Type: "fakeengine", Database: "main"}
	a := searchApp(conn)

	if _, err := a.GetDatabases(conn.ID); err != nil {
		t.Fatalf("schemas: %v", err)
	}
	drv, err := a.pluginDriver(conn)
	if err != nil {
		t.Fatalf("plugin driver: %v", err)
	}
	if drv == nil {
		t.Fatal("the plugin driver must be running")
	}
	if err := a.Disconnect(conn.ID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	// The driver is gone, so a fresh resolve starts a new process rather than
	// talking to a closed one.
	conn.pluginMu.Lock()
	still := conn.pluginConn
	conn.pluginMu.Unlock()
	if still != nil {
		t.Fatal("the plugin must be dropped on disconnect")
	}
}

// A plugin connection whose child was torn down must not look connected: the
// old isConnected only checked the registered type, so the next query silently
// restarted the plugin subprocess instead of failing with "not connected".
func TestPluginConnectionNotConnectedAfterDisconnect(t *testing.T) {
	buildFakeEnginePlugin(t)
	conn := &Connection{ID: "fake7", Name: "fake", Type: "fakeengine", Database: "main"}
	a := searchApp(conn)

	// Before any query the child is not running yet, but the connection is live.
	if !conn.isConnected() {
		t.Fatal("a connected plugin connection must report connected")
	}
	// Drive one query so the child actually starts.
	if _, err := a.GetDatabases(conn.ID); err != nil {
		t.Fatalf("schemas: %v", err)
	}
	if err := a.Disconnect(conn.ID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	// The map now holds a disconnectedHandle; it must not report connected.
	a.connectionsMu.RLock()
	handle := a.connections[conn.ID]
	a.connectionsMu.RUnlock()
	if handle == nil {
		t.Fatal("the handle must remain in the map after disconnect")
	}
	if handle.isConnected() {
		t.Fatal("a disconnected plugin connection must not report connected")
	}
	// And a query through it must fail rather than restart the plugin.
	if _, err := a.GetDatabases(conn.ID); err == nil {
		t.Fatal("querying a disconnected plugin connection must fail")
	}
}

// A plugin that declares a different engine than it is registered for is refused:
// this client would otherwise render its dialect wrong.
func TestPluginKindMismatchIsRefused(t *testing.T) {
	buildFakeEnginePlugin(t)
	// Same binary, registered under a different type.
	plugins.mu.Lock()
	spec := plugins.specs["fakeengine"]
	plugins.specs["mislabelled"] = pluginSpec{Path: spec.Path, Kind: "mislabelled"}
	plugins.mu.Unlock()

	conn := &Connection{ID: "fake4", Name: "mis", Type: "mislabelled", Database: "main"}
	_, err := conn.startPluginConn(pluginSpec{Path: spec.Path, Kind: "mislabelled"})
	if err == nil {
		t.Fatal("a plugin that claims another engine must be refused")
	}
	// The message names the engine the plugin actually speaks, so the user can fix
	// the registration instead of guessing.
	if !strings.Contains(err.Error(), "fakeengine") {
		t.Fatalf("err = %v", err)
	}
}

// Concurrent cold starts of the same plugin path must converge on ONE process:
// the registry lock guards bookkeeping only, the slow banner wait runs outside
// it (a wedged plugin used to queue every same-engine connection behind its
// 10s deadline). The loser retires its own process and joins the winner.
func TestAcquireHostConvergesConcurrentColdStarts(t *testing.T) {
	buildFakeEnginePlugin(t)
	spec := func() pluginSpec {
		plugins.mu.RLock()
		defer plugins.mu.RUnlock()
		return plugins.specs["fakeengine"]
	}()
	path := spec.Path

	const n = 4
	type result struct {
		host *plugindriver.ConnDriver
		err  error
	}
	results := make(chan result, n)
	var startGate sync.WaitGroup
	startGate.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			startGate.Done()
			startGate.Wait()
			host, err := pluginHosts.acquireHost(path, spec.Kind)
			results <- result{host, err}
		}()
	}
	seen := make(map[*plugindriver.ConnDriver]bool)
	for i := 0; i < n; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("acquireHost: %v", r.err)
		}
		seen[r.host] = true
	}
	if len(seen) != 1 {
		t.Fatalf("%d distinct hosts for one plugin path: concurrent cold starts did not converge", len(seen))
	}

	// Every caller's refcount lands on the one entry; releasing them all
	// closes the process (the temp dir cleanup needs that on Windows).
	pluginHosts.mu.Lock()
	entry := pluginHosts.hosts[hostKey(path, spec.Kind)]
	refs := entry.refCount
	pluginHosts.mu.Unlock()
	if refs != n {
		t.Fatalf("refCount = %d, want %d", refs, n)
	}
	for i := 0; i < n; i++ {
		pluginHosts.releaseHost(path, spec.Kind)
	}
	pluginHosts.mu.Lock()
	_, stillThere := pluginHosts.hosts[hostKey(path, spec.Kind)]
	pluginHosts.mu.Unlock()
	if stillThere {
		t.Fatal("the host must be closed once the last reference drops")
	}
}
