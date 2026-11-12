package main

// W17: the console-action API end to end, through a purpose-built fake plugin.
//
// The fake is deliberately minimal and dependency-free, so the test proves the CLIENT's
// wiring — the env declaration with arguments, the shared host keyed by those arguments,
// the two endpoints, and the result conversion — without needing the real console plugin
// (whose own module tests the providers against fake services).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dbcore/dbcore/plugindriver"
)

// fakeConsolePluginSource is a plugin that declares one console action and echoes its
// argument back. It picks its banner Kind from the first argv, which is what proves the
// ";arg" form actually reaches the executable.
const fakeConsolePluginSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

func main() {
	kind := "console"
	if len(os.Args) > 1 {
		kind = os.Args[1]
	}
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    kind,
		Name:    "Fake Console",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	in := bufio.NewReader(os.Stdin)
	var wmu sync.Mutex
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var req plugindriver.Request
			if json.Unmarshal(line, &req) == nil {
				go func(req plugindriver.Request) { reply(out, &wmu, req) }(req)
			}
		}
		if err != nil {
			return
		}
	}
}

func reply(out *bufio.Writer, wmu *sync.Mutex, req plugindriver.Request) {
	send := func(result interface{}, errText, code string) {
		payload, _ := json.Marshal(result)
		frame, _ := json.Marshal(plugindriver.Response{ID: req.ID, Result: payload, Error: errText, Code: code})
		wmu.Lock()
		fmt.Fprintf(out, "%s\n", frame)
		out.Flush()
		wmu.Unlock()
	}
	switch req.Method {
	case plugindriver.MethodPing:
		send("pong", "", "")
	case plugindriver.MethodOpen:
		send(plugindriver.OpenResult{ConnID: "c1", Server: "Fake Console"}, "", "")
	case "tables", plugindriver.MethodListConnTables:
		send([]plugindriver.ConnObject{{Name: "things", Kind: "collection"}}, "", "")
	case "columns", plugindriver.MethodListConnColumns:
		send([]plugindriver.ConnColumn{{Name: "id", Type: "TEXT"}}, "", "")
	case "browse", plugindriver.MethodConnBrowse:
		send(plugindriver.ConnQueryResult{
			Columns: []plugindriver.ConnColumn{{Name: "id", Type: "TEXT"}},
			Rows:    [][]interface{}{{"1"}},
		}, "", "")
	case plugindriver.MethodConnListActions:
		send([]plugindriver.ConnAction{{
			ID:     "echo",
			Title:  "Echo",
			Group:  "test",
			Params: []plugindriver.ConnActionParam{{Name: "msg", Label: "message"}},
		}}, "", "")
	case plugindriver.MethodConnRunAction:
		var p map[string]interface{}
		_ = json.Unmarshal(req.Params, &p)
		msg := ""
		if a, ok := p["args"].(map[string]interface{}); ok {
			if s, ok := a["msg"].(string); ok {
				msg = s
			}
		}
		send(plugindriver.ConnQueryResult{
			Columns: []plugindriver.ConnColumn{{Name: "echo", Type: "TEXT"}},
			Rows:    [][]interface{}{{msg}},
		}, "", "")
	case plugindriver.MethodClose:
		send(nil, "", "")
		os.Exit(0)
	default:
		send(nil, "unknown method "+req.Method, dbcore.ErrCodeNotSupported)
	}
}
`

// buildFakeConsolePlugin writes and builds the fake plugin in a temp module.
func buildFakeConsolePlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fakeConsolePluginSource), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "dbcore"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	goMod := "module fakeconsole\n\ngo 1.25\n\nrequire github.com/dbcore/dbcore v0.0.0\n\n" +
		"replace github.com/dbcore/dbcore => " + filepath.ToSlash(repoRoot) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	binary := filepath.Join(dir, "fakeconsole")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}
	return binary
}

// registerConsolePlugin registers the fake under one engine type WITH an argument, the
// form the console plugin uses to pick its provider.
func registerConsolePlugin(t *testing.T, binary string) {
	t.Helper()
	plugins.mu.Lock()
	plugins.specs["console"] = pluginSpec{Path: binary, Kind: "console", Args: []string{"console"}}
	plugins.mu.Unlock()
	t.Cleanup(func() { plugins.unregister("console") })
}

func consoleTestApp(connID string) (*App, *Connection) {
	conn := &Connection{ID: connID, Name: "console", Type: "console"}
	app := &App{connections: map[string]*Connection{connID: conn}}
	return app, conn
}

func doConsole(t *testing.T, call func(w http.ResponseWriter, r *http.Request), body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/plugin/action", strings.NewReader(body))
	rec := httptest.NewRecorder()
	call(rec, req)
	return rec
}

func TestPluginConsoleAPI(t *testing.T) {
	binary := buildFakeConsolePlugin(t)
	registerConsolePlugin(t, binary)

	app, conn := consoleTestApp("p1")
	defer conn.closePlugin()

	// The action list comes from the plugin, not the host.
	rec := doConsole(t, app.handlePluginActions, `{"connId":"p1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("actions status = %d: %s", rec.Code, rec.Body.String())
	}
	var actions []plugindriver.ConnAction
	if err := json.Unmarshal(rec.Body.Bytes(), &actions); err != nil {
		t.Fatalf("actions JSON: %v (%s)", err, rec.Body.String())
	}
	if len(actions) != 1 || actions[0].ID != "echo" || len(actions[0].Params) != 1 {
		t.Fatalf("actions = %+v", actions)
	}

	// Running it passes the args through and returns a query-shaped result the existing
	// grid can render: the echo proves the args reached the child process.
	rec = doConsole(t, app.handlePluginRunAction, `{"connId":"p1","action":"echo","args":{"msg":"hello-console"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("run status = %d: %s", rec.Code, rec.Body.String())
	}
	var qr QueryResult
	if err := json.Unmarshal(rec.Body.Bytes(), &qr); err != nil {
		t.Fatalf("run JSON: %v (%s)", err, rec.Body.String())
	}
	if qr.RowCount != 1 || len(qr.Rows) != 1 || qr.Rows[0][0] != "hello-console" {
		t.Fatalf("run result = %+v", qr)
	}
	if len(qr.Columns) != 1 || qr.Columns[0].Name != "echo" {
		t.Fatalf("run columns = %+v", qr.Columns)
	}
}

func TestPluginConsoleRefusesNonPluginConnection(t *testing.T) {
	app := &App{connections: map[string]*Connection{
		"s1": {ID: "s1", Name: "sqlite", Type: "sqlite"},
	}}
	rec := doConsole(t, app.handlePluginActions, `{"connId":"s1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-plugin connection = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	// And an unknown connection is refused too, rather than answered with silence.
	rec = doConsole(t, app.handlePluginActions, `{"connId":"nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown connection = %d, want 400", rec.Code)
	}
}

func TestPluginConsoleRunRequiresAction(t *testing.T) {
	app, conn := consoleTestApp("p2")
	defer conn.closePlugin()
	rec := doConsole(t, app.handlePluginRunAction, `{"connId":"p2"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing action = %d, want 400", rec.Code)
	}
}

// TestPluginHostKeyIncludesArgs pins the identity rule: the same binary launched with
// different arguments is a different engine and must not share a host.
func TestPluginHostKeyIncludesArgs(t *testing.T) {
	// Same binary, same declared kind, DIFFERENT arguments: the two are different
	// engines (Milvus vs Kafka) and must not share a host.
	a := hostKey(`C:\p\console.exe`, "console", []string{"milvus"})
	b := hostKey(`C:\p\console.exe`, "console", []string{"kafka"})
	if a == b {
		t.Fatal("two providers of one binary must not share a host key")
	}
	if hostKey("p", "k", nil) != hostKey("p", "k", []string{}) {
		t.Fatal("nil and empty args must key the same")
	}
}