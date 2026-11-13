package workbenchhost

// W-T1 acceptance: the terminal tab is RECONNECTABLE as a corollary of the
// architecture (AD-2), not a patched special case.
//
//   1. Kill/recover mechanics (a fake plugin built in-test, fast): launch →
//      Active with the nav node and channel pool registered → HARD-KILL the
//      process → the fiber deactivates and the accumulator reclaims EVERYTHING
//      → relaunch → a NEW fiber uid re-registers the SAME tags.
//   2. Real PTY end-to-end (the REAL navop-ssh binary against an in-process
//      sshd): open a channel → type → read the shell's echo → close. This is
//      the full chain: host runner ↔ plugin process ↔ sshx.DialChain ↔ sshd.
//
// Both paths drive the SAME runner API, which is the point: the host cannot
// tell a test plugin from navop-ssh — the protocol is the only contract.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

// fakeSSHPlugin is a minimal workbench plugin for the kill/recover mechanics:
// two acquisition effects (nav node + channel pool), an echo channel, and
// nothing else. Its behaviour mirrors navop-ssh's protocol surface exactly.
const fakeSSHPluginSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/dbcore/dbcore/plugindriver"
	"github.com/dbcore/dbcore/plugindriver/workbench"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto: plugindriver.ProtocolName, Version: plugindriver.ProtocolVersion,
		Kind: "fake-ssh", Name: "Fake SSH",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	in := bufio.NewReader(os.Stdin)
	var wmu sync.Mutex
	seq := 0
	known := map[string]bool{}
	handle := func(req plugindriver.Request) {
		send := func(result interface{}, errText, code string) {
			payload, _ := json.Marshal(result)
			resp := plugindriver.Response{ID: req.ID, Result: payload, Error: errText, Code: code}
			frame, _ := json.Marshal(resp)
			wmu.Lock()
			fmt.Fprintf(out, "%s\n", frame)
			out.Flush()
			wmu.Unlock()
		}
		switch req.Method {
		case plugindriver.MethodPing:
			send("pong", "", "")
		case workbench.MethodHello:
			send(workbench.Banner{
				Stream: workbench.StreamFrameV1, PluginID: "fake-ssh", ProviderID: "fake-1",
				Depends: []workbench.Capability{{Key: "connections.store", Commutative: true}},
				Provides: []workbench.Capability{
					{Key: "terminal.channels", Commutative: true},
					{Key: "files.session", Commutative: true},
				},
			}, "", "")
		case workbench.MethodEffect:
			var p struct{ Stage int }
			_ = json.Unmarshal(req.Params, &p)
			var frame workbench.EffectFrame
			switch p.Stage {
			case 0:
				payload, _ := json.Marshal(map[string]string{"label": "终端"})
				frame = workbench.EffectFrame{Tag: "fake-ssh/terminal-tab", Key: "nav.nodes", Op: "insert", Payload: payload, Inverse: "remove", Boundary: workbench.BoundaryAcquisition}
			case 1:
				payload, _ := json.Marshal(map[string]string{"kind": "ssh-pool"})
				frame = workbench.EffectFrame{Tag: "fake-ssh/channel-pool", Key: "terminal.channels", Op: "insert", Payload: payload, Inverse: "remove", Boundary: workbench.BoundaryAcquisition}
			default:
				frame = workbench.EffectFrame{Done: true}
			}
			send(frame, "", "")
		case workbench.MethodChannelOpen:
			// Generator-allocated id (Thm 47): two opens NEVER share a tag.
			seq++
			id := fmt.Sprintf("ch-%d", seq)
			known[id] = true
			send(map[string]string{"channelId": id}, "", "")
		case workbench.MethodChannelSize:
			send(map[string]bool{"ok": true}, "", "")
		case workbench.MethodChannelClose:
			var p map[string]interface{}
			_ = json.Unmarshal(req.Params, &p)
			cid, _ := p["channelId"].(string)
			delete(known, cid)
			send(map[string]bool{"ok": true}, "", "")
		case workbench.MethodFilesAction:
			// The fake answers everything; the HOST's realm gate is what the realm
			// tests assert (a refusal there never reaches this handler).
			var p map[string]interface{}
			_ = json.Unmarshal(req.Params, &p)
			action, _ := p["action"].(string)
			switch action {
			case "open":
				seq++
				send(map[string]string{"channelId": fmt.Sprintf("fs-%d", seq)}, "", "")
			case "list":
				send(map[string]interface{}{"entries": []interface{}{}}, "", "")
			default:
				send(map[string]bool{"ok": true}, "", "")
			}
		case workbench.MethodChannelData:
			// map decode: a struct tag's backtick would terminate THIS file's raw
			// string (it did exactly that once).
			var p map[string]interface{}
			_ = json.Unmarshal(req.Params, &p)
			// W-C1: the container actions ride the same channel-data method, so
			// the fake answers them by the action field before falling back to
			// terminal keystrokes.
			if action, _ := p["action"].(string); action != "" {
				switch action {
				case "ps":
					send(map[string]interface{}{"containers": []interface{}{
						map[string]interface{}{"id": "aaa111", "name": "web", "image": "nginx:1", "state": "running"},
						map[string]interface{}{"id": "bbb222", "name": "db", "image": "pg:16", "state": "exited"},
					}}, "", "")
				case "kubectl":
					send(map[string]interface{}{"output": "NAME   READY\nweb-0  1/1\n"}, "", "")
				case "exec":
					seq++
					send(map[string]interface{}{"channelId": fmt.Sprintf("cexec-%d", seq)}, "", "")
				case "exec-data":
					send(map[string]interface{}{"output": "shell-ok\n", "ended": false}, "", "")
				case "exec-close":
					send(map[string]bool{"ok": true}, "", "")
				default:
					send(nil, "unknown container action "+action, "ERR_NOT_SUPPORTED")
				}
				return
			}
			cid, _ := p["channelId"].(string)
			input, _ := p["input"].(string)
			if !known[cid] {
				send(nil, "unknown channel "+cid, "ERR_PLUGIN")
				return
			}
			send(map[string]string{"channelId": cid, "output": strings.ToUpper(input)}, "", "")
		case plugindriver.MethodClose:
			send(nil, "", "")
			os.Exit(0)
		default:
			send(nil, "unknown method "+req.Method, "ERR_NOT_SUPPORTED")
		}
	}
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var req plugindriver.Request
			if json.Unmarshal(line, &req) == nil {
				handle(req)
			}
		}
		if err != nil {
			return
		}
	}
}
`

func buildFakeSSHPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fakeSSHPluginSource), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "dbcore"))
	if err != nil {
		t.Fatal(err)
	}
	goMod := "module fakesh\n\ngo 1.25\n\nrequire github.com/dbcore/dbcore v0.1.0\n\n" +
		"replace github.com/dbcore/dbcore => " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "fakesh")
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

// killForTest is the hard kill the acceptance needs: the process dies
// mid-session with no chance to clean up — exactly the failure the accumulator
// must survive (Thm 68 despite arbitrary failure).
func (r *Runner) killForTest() error {
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil {
		return fmt.Errorf("no plugin process")
	}
	return p.kill()
}

// waitFiberState polls until the fiber reaches the wanted state (transitions run
// through Settle, which is synchronous, but the death watch is a goroutine).
func waitFiberState(t *testing.T, f *ctxcore.Fiber, want ctxcore.FiberState, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f.State() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fiber state = %s, want %s (reason=%q)", f.State(), want, f.Reason)
}

func navTags(s *ctxcore.Store) []string {
	var out []string
	for _, b := range s.Sigma()[ctxcore.KeyNavNodes] {
		out = append(out, string(b.Tag))
	}
	return out
}

func chanTags(s *ctxcore.Store) []string {
	var out []string
	for _, b := range s.Sigma()[ctxcore.KeyTerminalChannels] {
		out = append(out, string(b.Tag))
	}
	return out
}

func TestKillAndRecover(t *testing.T) {
	binary := buildFakeSSHPlugin(t)
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}

	if err := r.Launch(binary); err != nil {
		t.Fatalf("launch: %v", err)
	}
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 5*time.Second)
	uid1 := f.UID()

	// The load registered the nav node and the pool (the plugin's frames applied
	// host-side with the plugin's tags).
	if got := navTags(store); len(got) != 1 || !strings.Contains(got[0], "terminal-tab") {
		t.Fatalf("nav tags = %v", got)
	}
	if got := chanTags(store); len(got) != 1 || !strings.Contains(got[0], "channel-pool") {
		t.Fatalf("channel tags = %v", got)
	}

	// A session channel opened AFTER activation is a runtime effect, tracked on
	// the same accumulator.
	ch, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	out, _, err := r.WriteChannel(ch, "ping", 4096)
	if err != nil {
		t.Fatalf("write channel: %v", err)
	}
	if out != "PING" {
		t.Fatalf("echo = %q", out)
	}
	if got := chanTags(store); len(got) != 2 {
		t.Fatalf("after open: %v", got)
	}

	// THE ACCEPTANCE: hard-kill the process. Death is a push (proc-exit channel)
	// → retire → Settle → the accumulator reclaims the nav node, the pool AND
	// the session channel — despite the kill happening mid-session.
	if err := r.killForTest(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitFiberState(t, f, ctxcore.Inactive, 10*time.Second)
	if !f.Retired() {
		t.Fatal("the killed fiber must be retired (τ monotone)")
	}
	if got := navTags(store); len(got) != 0 {
		t.Fatalf("Cor 69 violated: nav tags survived the kill: %v", got)
	}
	if got := chanTags(store); len(got) != 0 {
		t.Fatalf("Cor 69 violated: channel tags survived the kill: %v", got)
	}

	// Relaunch: a NEW fiber (fresh uid — τ never resurrects) re-registers the
	// SAME tags. The terminal tab comes back exactly as it was.
	if err := r.Launch(binary); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	f2 := r.Fiber()
	waitFiberState(t, f2, ctxcore.Active, 5*time.Second)
	if f2.UID() == uid1 {
		t.Fatal("the relaunched fiber must have a fresh uid (Thm 80 / Lemma 59(5))")
	}
	if got := navTags(store); len(got) != 1 || !strings.Contains(got[0], "terminal-tab") {
		t.Fatalf("nav tags after relaunch = %v (the tab must be restored)", got)
	}
	// And the channel surface is usable again.
	ch2, err := r.OpenChannel(map[string]any{"host": "x"})
	if err != nil {
		t.Fatalf("open channel after relaunch: %v", err)
	}
	if out, _, err := r.WriteChannel(ch2, "again", 4096); err != nil || out != "AGAIN" {
		t.Fatalf("echo after relaunch = %q err=%v", out, err)
	}
	r.Stop()
}

// ---------- real PTY end-to-end ----------

// buildNavopSSH builds the REAL plugin binary from plugins/navop-ssh.
func buildNavopSSH(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "navop-ssh")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// navop-ssh is its OWN module (it must build without the host), so the build
	// runs in the plugin's directory, not from the main module.
	pluginDir := filepath.Join(root, "plugins", "navop-ssh")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = pluginDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build navop-ssh: %v\n%s", err, out)
	}
	return binary
}

func TestRealPTYEndToEnd(t *testing.T) {
	binary := buildNavopSSH(t)
	sshHost, sshPort := testSSHServer(t, "tester", "secret")
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatalf("launch navop-ssh: %v", err)
	}
	f := r.Fiber()
	waitFiberState(t, f, ctxcore.Active, 10*time.Second)
	if got := navTags(store); len(got) != 1 {
		t.Fatalf("nav tags = %v", got)
	}

	ch, err := r.OpenChannel(map[string]any{
		"host": sshHost, "port": sshPort, "user": "tester", "password": "secret",
		"cols": 80, "rows": 24,
	})
	if err != nil {
		t.Fatalf("open PTY channel: %v", err)
	}
	// Type a command the in-process server answers; poll for the echo.
	var got string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, ended, err := r.WriteChannel(ch, "ping-through-pty\r\n", 4096)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		got += out
		if strings.Contains(got, "pong-through-pty") {
			break
		}
		if ended {
			t.Fatalf("the PTY ended before echoing: %q", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(got, "pong-through-pty") {
		t.Fatalf("PTY echo missing: %q", got)
	}
	if err := r.ResizeChannel(ch, 100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if err := r.CloseChannel(ch); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := chanTags(store); len(got) != 1 || !strings.Contains(got[0], "channel-pool") {
		t.Fatalf("after close: %v (the session binding must be reclaimed, the pool stays)", got)
	}
	r.Stop()
}
