package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
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

// Every tunnelled connection runs two io.Copy goroutines. The pipe's signal
// channel is buffered for exactly two sends, so both copiers can finish even
// when only one direction ends first. This test pins that property: a future
// change that drops the buffer to one, or waits for both directions, would
// park a goroutine per tunnelled connection for the life of the process.
//
// (Written while chasing a suspected leak that turned out not to exist — the
// original buffered-by-two channel is correct. Kept as the regression guard
// the next reader would otherwise have to re-derive.)
//
// The test servers are started ONCE, outside the loop: their accept loops and
// per-connection handlers would otherwise be counted as the tunnel's leak.
func TestSSHTunnelDoesNotLeakCopierGoroutines(t *testing.T) {
	echoHost, echoPort := startEchoServer(t)
	sshHost, sshPort := startSSHServer(t, "tester", "secret")
	// Settle the server goroutines before taking the baseline.
	time.Sleep(100 * time.Millisecond)
	base := runtime.NumGoroutine()

	const rounds = 20
	for i := 0; i < rounds; i++ {
		tun, lh, lp, err := openSSHTunnel(SSHConfig{
			Enabled: true, Host: sshHost, Port: sshPort, User: "tester", Password: "secret",
		}, echoHost, echoPort)
		if err != nil {
			t.Fatalf("round %d: open tunnel: %v", i, err)
		}
		conn, err := net.Dial("tcp", net.JoinHostPort(lh, strconv.Itoa(lp)))
		if err != nil {
			tun.Close()
			t.Fatalf("round %d: dial: %v", i, err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte("x"))
		buf := make([]byte, 1)
		_, _ = io.ReadFull(conn, buf)
		_ = conn.Close()
		tun.Close()
	}
	// Give the copiers a moment to unwind after their conns closed.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base+rounds/2 { // slack for server-side per-conn handlers
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutines grew from %d to %d over %d tunnels (copier leak)",
		base, runtime.NumGoroutine(), rounds)
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

// W4-b: a two-hop chain. The bastion is a real in-process SSH server, and the
// second hop is dialled THROUGH it — the jump host's address is only reachable
// from inside the bastion's network in production, and here it proves the
// channel is what carries the second handshake.
func TestSSHTunnelThroughBastion(t *testing.T) {
	echoHost, echoPort := startEchoServer(t)
	// The jump host (second hop) is another in-process SSH server, standing in
	// for a host reachable only from the bastion.
	jumpHost, jumpPort := startSSHServer(t, "jump", "jumppw")
	// The bastion (first hop).
	bastionHost, bastionPort := startSSHServer(t, "bounce", "bouncepw")

	tun, host, port, err := openSSHTunnel(SSHConfig{
		Enabled:  true,
		Host:     jumpHost,
		Port:     jumpPort,
		User:     "jump",
		Password: "jumppw",
		Bastion: &SSHConfig{
			Enabled:  true,
			Host:     bastionHost,
			Port:     bastionPort,
			User:     "bounce",
			Password: "bouncepw",
		},
	}, echoHost, echoPort)
	if err != nil {
		t.Fatalf("two-hop tunnel: %v", err)
	}
	defer tun.Close()

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("through-both-hops")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len("through-both-hops"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "through-both-hops" {
		t.Fatalf("echo = %q", buf)
	}
}

// A bastion whose credentials are wrong must fail at the FIRST hop, and the
// error must say so — otherwise the user debugs the jump host that never ran.
func TestSSHTunnelBastionFailureNamesTheHop(t *testing.T) {
	echoHost, echoPort := startEchoServer(t)
	jumpHost, jumpPort := startSSHServer(t, "jump", "jumppw")
	bastionHost, bastionPort := startSSHServer(t, "bounce", "rightpw")

	_, _, _, err := openSSHTunnel(SSHConfig{
		Enabled:  true,
		Host:     jumpHost,
		Port:     jumpPort,
		User:     "jump",
		Password: "jumppw",
		Bastion: &SSHConfig{
			Enabled:  true,
			Host:     bastionHost,
			Port:     bastionPort,
			User:     "bounce",
			Password: "wrongpw",
		},
	}, echoHost, echoPort)
	if err == nil {
		t.Fatal("a bad bastion credential must fail")
	}
	if !strings.Contains(err.Error(), "跳板机") {
		t.Fatalf("error should name the bastion hop: %v", err)
	}
}

// A bastion config that itself carries a proxy is refused: the second hop
// cannot be reached through this machine's proxy, and honouring the field
// silently would connect somewhere the user did not ask for.
func TestSSHConfigRejectsProxyOnBastion(t *testing.T) {
	cfg := SSHConfig{
		Enabled:  true,
		Host:     "jump",
		User:     "u",
		Password: "p",
		Bastion: &SSHConfig{
			Enabled:  true,
			Host:     "bastion",
			User:     "b",
			Password: "b",
			Proxy:    &ProxyConfig{Enabled: true, Host: "proxy", Port: 1080},
		},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("a proxy on the bastion must be refused")
	}
}

func TestSSHConfigValidatesProxyFields(t *testing.T) {
	base := SSHConfig{Enabled: true, Host: "h", User: "u", Password: "p"}
	bad := base
	bad.Proxy = &ProxyConfig{Enabled: true} // no host
	if err := bad.validate(); err == nil {
		t.Fatal("a proxy without a host must be refused")
	}
	badPort := base
	badPort.Proxy = &ProxyConfig{Enabled: true, Host: "proxy", Port: 70000}
	if err := badPort.validate(); err == nil {
		t.Fatal("an out-of-range proxy port must be refused")
	}
	good := base
	good.Proxy = &ProxyConfig{Enabled: true, Host: "proxy", Port: 1080}
	if err := good.validate(); err != nil {
		t.Fatalf("a valid proxy should pass: %v", err)
	}
}
