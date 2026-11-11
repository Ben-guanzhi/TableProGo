package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHConfig is the jump-host ("bastion") configuration saved with a connection.
// It is intentionally a plain struct: it round-trips through the same state file
// as the rest of ConnectionConfig, and the password/key passphrase are stored
// through the same secrets layer as the database password (see secrets.go).
type SSHConfig struct {
	Enabled    bool   `json:"enabled"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password"`
	KeyFile    string `json:"keyFile"`
	Passphrase string `json:"passphrase"`
	// W4-b: an optional first hop (the bastion), reached before Host. A
	// two-hop chain is what "jump host, then internal host" needs; a third hop
	// is deliberately out of scope — it changes the failure modes the user has
	// to debug (which hop refused?) without adding a case a second hop does not
	// already cover.
	Bastion *SSHConfig `json:"bastion,omitempty"`
	// W4-c: a SOCKS5 proxy to reach the SSH host through. It applies to the
	// FIRST hop (the bastion when one is configured, else the jump host),
	// because that is the only connection made from this machine.
	Proxy *ProxyConfig `json:"proxy,omitempty"`
	// W4-a: when non-empty, the host/user/port/key below were prefilled from
	// ~/.ssh/config for this alias; recording it lets the dialog say where the
	// values came from and lets "re-read" work.
	ConfigAlias string `json:"configAlias,omitempty"`
}

// ProxyConfig is a SOCKS5 endpoint used to reach the SSH host.
type ProxyConfig struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	// RemoteDNS asks the proxy to resolve the target name (SOCKS5 ATYP=domain).
	// On by default: the reason to use a proxy is usually that this machine
	// cannot resolve or reach the internal name.
	RemoteDNS bool `json:"remoteDns"`
}

// validate reports configuration errors before any network work happens, so the
// dialog can show "missing user" instead of a dial timeout.
func (c SSHConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Host == "" {
		return fmt.Errorf("ssh: host is required")
	}
	if c.User == "" {
		return fmt.Errorf("ssh: user is required")
	}
	if c.KeyFile == "" && c.Password == "" {
		return fmt.Errorf("ssh: a password or a private key file is required")
	}
	if c.Proxy != nil && c.Proxy.Enabled {
		if c.Proxy.Host == "" {
			return fmt.Errorf("ssh: 代理主机是必填项")
		}
		if c.Proxy.Port <= 0 || c.Proxy.Port > 65535 {
			return fmt.Errorf("ssh: 代理端口无效: %d", c.Proxy.Port)
		}
	}
	// A bastion must itself be a complete config, and it may NOT carry a proxy:
	// the second hop is reached through the bastion's own channel, so a proxy
	// field there could not be honoured — silently ignoring it would connect
	// somewhere the user did not ask for.
	if c.Bastion != nil && c.Bastion.Enabled {
		if c.Bastion.Proxy != nil && c.Bastion.Proxy.Enabled {
			return fmt.Errorf("跳板机不能再配代理：第二跳是通过跳板机的 SSH 通道完成的，代理字段无法生效")
		}
		if c.Bastion.Bastion != nil && c.Bastion.Bastion.Enabled {
			return fmt.Errorf("跳板机只支持两跳（跳板机 → 主机），不支持三层嵌套")
		}
		nested := *c.Bastion
		nested.Enabled = true
		nested.Bastion = nil
		nested.Proxy = nil
		if err := nested.validate(); err != nil {
			return fmt.Errorf("跳板机配置: %w", err)
		}
	}
	return nil
}

// sshTunnel is one local port-forward through one or more jump hosts: a loopback
// listener whose accepted connections are carried over an SSH "direct-tcpip"
// channel to the database endpoint.
type sshTunnel struct {
	// clients is every SSH connection in the chain, outermost last. Closing in
	// reverse order tears the innermost down first, which is the order that
	// lets each hop's channel close on a live parent.
	clients  []*ssh.Client
	listener net.Listener
	remote   string
}

// openSSHTunnel connects to the jump host (and, when configured, a bastion
// first) and starts forwarding to remoteHost:remotePort. It returns the loopback
// address the drivers should dial.
func openSSHTunnel(cfg SSHConfig, remoteHost string, remotePort int) (*sshTunnel, string, int, error) {
	if err := cfg.validate(); err != nil {
		return nil, "", 0, err
	}
	if remoteHost == "" {
		remoteHost = "127.0.0.1"
	}
	if remotePort == 0 {
		return nil, "", 0, fmt.Errorf("ssh: remote database port is required")
	}

	clients, err := dialSSHChain(cfg)
	if err != nil {
		return nil, "", 0, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		closeClients(clients)
		return nil, "", 0, fmt.Errorf("ssh: local listener: %w", err)
	}
	t := &sshTunnel{
		clients:  clients,
		listener: listener,
		remote:   net.JoinHostPort(remoteHost, strconv.Itoa(remotePort)),
	}
	go t.forward()
	addr := listener.Addr().(*net.TCPAddr)
	return t, addr.IP.String(), addr.Port, nil
}

// dialSSHChain opens the SSH connection(s), bastion first when configured, and
// returns every client in the chain (outermost first) so the tunnel can close
// them in reverse order.
//
// A two-hop chain is built the only way it can be: the second hop is dialled
// *through* the first hop's transport, so the inner connection never touches
// this machine's network stack.
func dialSSHChain(cfg SSHConfig) ([]*ssh.Client, error) {
	dialerFor := func(c SSHConfig) (sshDialer, error) {
		if c.Proxy != nil && c.Proxy.Enabled {
			return socks5Dialer(*c.Proxy)
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext, nil
	}

	if cfg.Bastion == nil || !cfg.Bastion.Enabled {
		dial, err := dialerFor(cfg)
		if err != nil {
			return nil, err
		}
		inner, err := dialSSHOnce(cfg, dial)
		if err != nil {
			return nil, err
		}
		return []*ssh.Client{inner}, nil
	}

	// First hop: the bastion, reached with this machine's own network (plus the
	// optional SOCKS proxy).
	bastionDial, err := dialerFor(*cfg.Bastion)
	if err != nil {
		return nil, err
	}
	bastion, err := dialSSHOnce(*cfg.Bastion, bastionDial)
	if err != nil {
		return nil, fmt.Errorf("跳板机: %w", err)
	}

	// Second hop: the jump host, dialled through the bastion's direct-tcpip
	// channel at the JUMP HOST's address. The proxy is not re-applied here — it
	// cannot be, and a config that tries is rejected by validate().
	jumpTarget := net.JoinHostPort(cfg.Host, strconv.Itoa(sshPort(cfg)))
	throughBastion := func(_ context.Context, network, _ string) (net.Conn, error) {
		return bastion.Dial(network, jumpTarget)
	}
	inner, err := dialSSHOnceAt(cfg, throughBastion, jumpTarget)
	if err != nil {
		_ = bastion.Close()
		return nil, fmt.Errorf("跳板机已连接，但到 %s 的第二跳失败: %w", jumpTarget, err)
	}
	return []*ssh.Client{bastion, inner}, nil
}

type sshDialer = func(ctx context.Context, network, addr string) (net.Conn, error)

// sshPort is the config's port or the SSH default.
func sshPort(c SSHConfig) int {
	if c.Port > 0 {
		return c.Port
	}
	return 22
}

// dialSSHOnce opens one SSH connection to cfg's own address.
func dialSSHOnce(cfg SSHConfig, dial sshDialer) (*ssh.Client, error) {
	return dialSSHOnceAt(cfg, dial, net.JoinHostPort(cfg.Host, strconv.Itoa(sshPort(cfg))))
}

// dialSSHOnceAt opens one SSH connection whose transport is already positioned
// at target. The transport and the handshake target are separate because a
// chained hop dials a fixed address (the next hop) rather than through a
// per-address dialer.
func dialSSHOnceAt(cfg SSHConfig, dial sshDialer, target string) (*ssh.Client, error) {
	auth, err := sshAuthMethods(cfg)
	if err != nil {
		return nil, err
	}
	conn, err := dial(context.Background(), "tcp", target)
	if err != nil {
		return nil, fmt.Errorf("ssh: connect to %s: %w", target, err)
	}
	// A desktop client cannot ask the user to verify a fingerprint on every
	// connect, so the host key is accepted as-is (same trade-off as the
	// reference clients). Documented here rather than silently implied.
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, target, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh: handshake with %s: %w", target, err)
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// sshAuthMethods prefers key auth when a key file is configured and falls back
// to the password; both may be present (some bastions require 2FA-style pairs).
func sshAuthMethods(cfg SSHConfig) ([]ssh.AuthMethod, error) {
	var auth []ssh.AuthMethod
	if cfg.KeyFile != "" {
		key, err := os.ReadFile(cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("ssh: read key %q: %w", cfg.KeyFile, err)
		}
		var signer ssh.Signer
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("ssh: parse key %q: %w", cfg.KeyFile, err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		auth = append(auth, ssh.Password(cfg.Password))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("ssh: a password or a private key file is required")
	}
	return auth, nil
}

// forward accepts local connections until the listener closes and pipes each one
// to the remote database through the innermost SSH connection.
func (t *sshTunnel) forward() {
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return
		}
		go t.pipe(local)
	}
}

// inner is the connection that reaches the database: the last hop in the chain.
func (t *sshTunnel) inner() *ssh.Client {
	if len(t.clients) == 0 {
		return nil
	}
	return t.clients[len(t.clients)-1]
}

func (t *sshTunnel) pipe(local net.Conn) {
	client := t.inner()
	if client == nil {
		_ = local.Close()
		return
	}
	remote, err := client.Dial("tcp", t.remote)
	if err != nil {
		_ = local.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
	_ = local.Close()
	_ = remote.Close()
}

// Close tears down the listener and every SSH connection in the chain
// (idempotent). The order is innermost first: closing a hop before the channel
// that rides on it turns a clean shutdown into a broken-pipe error in the logs.
func (t *sshTunnel) Close() {
	if t == nil {
		return
	}
	if t.listener != nil {
		_ = t.listener.Close()
	}
	closeClients(t.clients)
	t.clients = nil
}

// closeClients closes a chain innermost-first.
func closeClients(clients []*ssh.Client) {
	for i := len(clients) - 1; i >= 0; i-- {
		if clients[i] != nil {
			_ = clients[i].Close()
		}
	}
}
