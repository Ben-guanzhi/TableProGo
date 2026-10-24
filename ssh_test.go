package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startEchoServer returns a TCP server that echoes everything it reads.
func startEchoServer(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// startSSHServer runs a minimal in-process SSH server that accepts password
// auth and serves "direct-tcpip" forwarding (what an SSH tunnel needs). It lets
// the tunnel be tested end-to-end without an external sshd.
func startSSHServer(t *testing.T, user, password string) (host string, port int) {
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
			go serveSSHConn(conn, cfg)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func serveSSHConn(conn net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "direct-tcpip" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only direct-tcpip is supported")
			continue
		}
		var target struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(newCh.ExtraData(), &target); err != nil {
			_ = newCh.Reject(ssh.ConnectionFailed, "bad target")
			continue
		}
		remote, err := net.Dial("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
		if err != nil {
			_ = newCh.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, reqs, err := newCh.Accept()
		if err != nil {
			_ = remote.Close()
			continue
		}
		go ssh.DiscardRequests(reqs)
		go func() { _, _ = io.Copy(ch, remote); _ = ch.Close() }()
		go func() { _, _ = io.Copy(remote, ch); _ = remote.Close() }()
	}
}

func TestSSHConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  SSHConfig
		ok   bool
	}{
		{"disabled", SSHConfig{}, true},
		{"missing host", SSHConfig{Enabled: true, User: "u", Password: "p"}, false},
		{"missing user", SSHConfig{Enabled: true, Host: "h", Password: "p"}, false},
		{"missing auth", SSHConfig{Enabled: true, Host: "h", User: "u"}, false},
		{"password ok", SSHConfig{Enabled: true, Host: "h", User: "u", Password: "p"}, true},
		{"key ok", SSHConfig{Enabled: true, Host: "h", User: "u", KeyFile: "k"}, true},
	}
	for _, c := range cases {
		err := c.cfg.validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

// The tunnel must carry bytes both ways through a real SSH handshake.
func TestSSHTunnelForwardsBytes(t *testing.T) {
	echoHost, echoPort := startEchoServer(t)
	sshHost, sshPort := startSSHServer(t, "tester", "secret")

	tun, host, port, err := openSSHTunnel(SSHConfig{
		Enabled: true, Host: sshHost, Port: sshPort, User: "tester", Password: "secret",
	}, echoHost, echoPort)
	if err != nil {
		t.Fatalf("open tunnel: %v", err)
	}
	defer tun.Close()

	conn, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping-through-ssh")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len("ping-through-ssh"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping-through-ssh" {
		t.Fatalf("echo = %q", buf)
	}
}

// The wrong password must fail the handshake, not hang.
func TestSSHTunnelRejectsBadPassword(t *testing.T) {
	echoHost, echoPort := startEchoServer(t)
	sshHost, sshPort := startSSHServer(t, "tester", "secret")

	_, _, _, err := openSSHTunnel(SSHConfig{
		Enabled: true, Host: sshHost, Port: sshPort, User: "tester", Password: "wrong",
	}, echoHost, echoPort)
	if err == nil {
		t.Fatal("expected authentication failure")
	}
}

// TestConnection must transparently route through the tunnel too: the dialog's
// "Test" button has to prove the same path "Connect" will use.
func TestLiveSSHViaTestConnection(t *testing.T) {
	raw := os.Getenv("TABULA_LIVE_PG")
	if raw == "" {
		t.Skip("TABULA_LIVE_PG not set")
	}
	cfg := connectionConfigFromURL(t, "postgresql", raw)
	sshHost, sshPort := startSSHServer(t, "tester", "secret")
	cfg.SSH = SSHConfig{Enabled: true, Host: sshHost, Port: sshPort, User: "tester", Password: "secret"}

	app := &App{}
	if err := app.TestConnection(cfg); err != nil {
		t.Fatalf("TestConnection through tunnel: %v", err)
	}
}

// A bad SSH credential must surface as a tunnel error, not as a DB timeout.
func TestLiveSSHBadCredentialFailsFast(t *testing.T) {
	raw := os.Getenv("TABULA_LIVE_PG")
	if raw == "" {
		t.Skip("TABULA_LIVE_PG not set")
	}
	cfg := connectionConfigFromURL(t, "postgresql", raw)
	sshHost, sshPort := startSSHServer(t, "tester", "secret")
	cfg.SSH = SSHConfig{Enabled: true, Host: sshHost, Port: sshPort, User: "tester", Password: "wrong"}

	start := time.Now()
	err := (&App{}).TestConnection(cfg)
	if err == nil {
		t.Fatal("expected an SSH authentication error")
	}
	if !strings.Contains(err.Error(), "ssh") {
		t.Fatalf("error should mention ssh, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("failure took %v, expected fast fail", elapsed)
	}
}

func TestSSHTunnelToLivePostgres(t *testing.T) {
	raw := os.Getenv("TABULA_LIVE_PG")
	if raw == "" {
		t.Skip("TABULA_LIVE_PG not set")
	}
	cfg := connectionConfigFromURL(t, "postgresql", raw)
	sshHost, sshPort := startSSHServer(t, "tester", "secret")

	tun, host, port, err := openSSHTunnel(SSHConfig{
		Enabled: true, Host: sshHost, Port: sshPort, User: "tester", Password: "secret",
	}, cfg.Host, cfg.Port)
	if err != nil {
		t.Fatalf("open tunnel: %v", err)
	}
	defer tun.Close()

	db, err := createConnectionAt(cfg, host, port)
	if err != nil {
		t.Fatalf("createConnectionAt: %v", err)
	}
	defer db.Close()
	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatalf("select through tunnel: %v", err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 = %d", one)
	}
}
