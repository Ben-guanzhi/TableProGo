package workbenchhost

// sshd_test.go — an in-process SSH server for the REAL PTY end-to-end test.
//
// The host's own tunnel tests use the same fixture shape (ed25519 host key +
// password auth, see ssh_test.go): a real ssh.ServerConfig on a loopback port.
// The session backend here is deliberately minimal but REAL — it runs the
// standard session lifecycle (pty-req → shell) and echoes stdin back with
// ping→pong rewritten, so the plugin's PTY plumbing (OpenPTY, the pump, resize,
// close) is exercised against an actual server, not a mock.

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

var (
	sshSrvMu     sync.Mutex
	sshSrvAddr   string
	sshSrvPasswd string
)

// testSSHServer starts one loopback sshd and returns its address. One server per
// call keeps tests independent.
func testSSHServer(t *testing.T, user, password string) (string, int) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(pass) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid credentials")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(conn, cfg, password)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// serveSSHConn runs one SSH connection: handshake, then serve "session" channels
// with the standard pty-req → shell handshake and an echo backend.
func serveSSHConn(conn net.Conn, cfg *ssh.ServerConfig, password string) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	_ = password
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			return
		}
		go serveSSHSession(ch, chReqs)
	}
}

// serveSSHSession answers pty-req/shell/window-change and echoes stdin, with
// ping→pong rewritten so the test can tell the backend's reply from its own
// input coming back through a mis-plumbed pipe.
func serveSSHSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var shellStarted bool
	for req := range reqs {
		ok := false
		switch req.Type {
		case "pty-req":
			ok = true
		case "shell":
			ok = shellStarted == false
			shellStarted = true
		case "window-change":
			ok = true
		}
		if req.WantReply {
			_ = req.Reply(ok, nil)
		}
		if req.Type == "shell" && ok {
			go echoLoop(ch)
		}
	}
}

// echoLoop reads the shell's stdin and writes it back with ping→pong rewritten.
func echoLoop(ch ssh.Channel) {
	buf := make([]byte, 4096)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			reply := strings.ReplaceAll(string(buf[:n]), "ping", "pong")
			_, _ = ch.Write([]byte(reply))
		}
		if err != nil {
			return
		}
	}
}
