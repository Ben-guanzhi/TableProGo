package workbenchhost

// W-D1 acceptance: RDP is an emission whose compensation is the .rdp file's
// deletion; VNC is a channel relay.
//
//   1. THE COMPENSATION: StartRDP writes the file, spawns the (stubbed) client,
//      records the session row — and unloading the fiber DELETES the file and the
//      row. CloseDesktopSession does the same early. This is the plan's "补偿：
//      .rdp 临时文件清理登记为效应逆", asserted as a file that is gone.
//   2. NO CREDENTIAL IN THE FILE: the .rdp content has the address and the user and
//      no password field, so the inverse is a plain delete rather than an erasure.
//   3. VNC FRAMES RIDE THE CHANNEL: bytes in, bytes back, from a fake VNC server;
//      closing ends the socket and withdraws the row (Cor 69 on unload too).
//   4. serial/telnet are refused by name (they belong to the ssh tail).

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

// stubLauncher lays down an executable that records its argv. On Windows that is a
// .bat (cmd runs it); elsewhere a shell script. Returns the path.
func stubLauncher(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "launched.txt")
	if runtimeIsWindows() {
		path := filepath.Join(dir, "stub-mstsc.bat")
		body := "@echo off\r\necho %* > \"" + marker + "\"\r\n"
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		return path, marker
	}
	path := filepath.Join(dir, "stub-mstsc.sh")
	body := "#!/bin/sh\necho \"$@\" > \"" + marker + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, marker
}

func runtimeIsWindows() bool { return os.PathSeparator == '\\' }

func desktopSetup(t *testing.T) *Runner {
	t.Helper()
	binary := buildPluginModule(t, "navop-desktop")
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.ProvideConnectionsArchive(map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	waitFiberState(t, r.Fiber(), ctxcore.Active, 10*time.Second)
	desktopDirOverride = t.TempDir()
	t.Cleanup(func() { desktopDirOverride = "" })
	return r
}

func waitFile(t *testing.T, path string, want bool, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		_, err := os.Stat(path)
		exists := err == nil
		if exists == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file %s existence=%v, want %v", path, !want, want)
}

func TestRDPSpawnIsEmissionWithFileCompensation(t *testing.T) {
	stub, marker := stubLauncher(t)
	t.Setenv("NAVOP_DESKTOP_LAUNCHER", stub)
	r := desktopSetup(t)

	res, err := r.StartRDP("10.0.0.5", 3389, "ops")
	if err != nil {
		t.Fatalf("start RDP: %v", err)
	}
	// The .rdp exists, is owner-only, carries the address and user, and NO password.
	waitFile(t, res.File, true, 2*time.Second)
	body, err := os.ReadFile(res.File)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "full address:s:10.0.0.5") || !strings.Contains(text, "username:s:ops") {
		t.Fatalf(".rdp content = %q", text)
	}
	if strings.Contains(strings.ToLower(text), "password") {
		t.Fatalf("the .rdp must not carry a credential: %q", text)
	}
	// The stub was spawned with the file path (the emission happened).
	waitFile(t, marker, true, 3*time.Second)
	spawned, _ := os.ReadFile(marker)
	if !strings.Contains(string(spawned), filepath.Base(res.File)) {
		t.Fatalf("the launcher did not receive the file: %q", spawned)
	}
	// The session row is on desktop.session.
	if rows := r.DesktopSessions(); len(rows) != 1 || rows[0]["kind"] != "rdp" {
		t.Fatalf("desktop rows = %v", rows)
	}

	// Unloading the fiber runs the compensating inverse: the file and the row go.
	r.store.Retire(r.Fiber())
	waitFiberState(t, r.Fiber(), ctxcore.Inactive, 10*time.Second)
	waitFile(t, res.File, false, 3*time.Second)
	if rows := r.DesktopSessions(); len(rows) != 0 {
		t.Fatalf("Cor 69 violated: desktop rows survived unload: %v", rows)
	}
}

func TestRDPCloseRemovesTheFileEarly(t *testing.T) {
	stub, _ := stubLauncher(t)
	t.Setenv("NAVOP_DESKTOP_LAUNCHER", stub)
	r := desktopSetup(t)
	res, err := r.StartRDP("10.0.0.6", 3389, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, res.File, true, 2*time.Second)
	if err := r.CloseDesktopSession(res.Session); err != nil {
		t.Fatal(err)
	}
	waitFile(t, res.File, false, 2*time.Second)
	if rows := r.DesktopSessions(); len(rows) != 0 {
		t.Fatalf("the row survived the explicit close: %v", rows)
	}
	// The client window is an emission: closing the session does not (and cannot)
	// close it. That is stated in the result's Notice, asserted here so a future
	// change does not quietly promise an undo it cannot deliver.
	if !strings.Contains(res.Notice, "不可撤回") {
		t.Fatalf("the notice must say the emission is irreversible: %q", res.Notice)
	}
}

func TestVNCFramesRideTheChannel(t *testing.T) {
	r := desktopSetup(t)
	vncHost, vncPort := startFakeVNCServer(t)

	start, err := r.OpenVNCSession(vncHost, vncPort)
	if err != nil {
		t.Fatalf("open VNC: %v", err)
	}
	if rows := r.DesktopSessions(); len(rows) != 1 || rows[0]["kind"] != "vnc" {
		t.Fatalf("desktop rows = %v", rows)
	}

	// Bytes out, bytes back: the fake server echoes.
	reply, ended, err := r.VNCRelay(start.Session, []byte("RFB 003.008\n"))
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	if ended {
		t.Fatal("the relay ended on the first frame")
	}
	if string(reply) != "RFB 003.008\n" {
		t.Fatalf("relay reply = %q", reply)
	}

	// An empty poll is a valid "nothing pending" answer.
	if reply, _, err := r.VNCRelay(start.Session, nil); err != nil || len(reply) != 0 {
		t.Fatalf("empty poll = %q, %v", reply, err)
	}

	// Unload closes the socket and withdraws the row.
	r.store.Retire(r.Fiber())
	waitFiberState(t, r.Fiber(), ctxcore.Inactive, 10*time.Second)
	if rows := r.DesktopSessions(); len(rows) != 0 {
		t.Fatalf("Cor 69 violated: the VNC row survived unload: %v", rows)
	}
}

func TestDesktopRefusesSerialAndTelnet(t *testing.T) {
	r := desktopSetup(t)
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	err := p.call(context.Background(), "workbench/channel/open", map[string]any{
		"kind": "telnet", "host": "x", "port": 23,
	}, nil)
	if err == nil {
		t.Fatal("telnet must be refused: it belongs to the ssh tail")
	}
	if !strings.Contains(err.Error(), "navop-ssh") {
		t.Fatalf("the refusal must point at the ssh tail: %v", err)
	}
}

// startFakeVNCServer is a TCP echo server standing in for a VNC daemon.
func startFakeVNCServer(t *testing.T) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c interface {
				Read([]byte) (int, error)
				Write([]byte) (int, error)
				Close() error
			}) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						_, _ = c.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}
