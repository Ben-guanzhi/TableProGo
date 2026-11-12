// Package sshx is the shared SSH remote-access library for the host AND its
// workbench plugins (W-T1 迁入: the dial chain, auth, OpenSSH-config parsing and
// the PTY session helper live here so navop-ssh and the host's DB-over-SSH
// tunnels build against ONE copy instead of diverging forks).
//
// It is deliberately a PUBLIC (non-internal) package of the host module: a
// workbench plugin is a separate Go module that builds against the host's shared
// code via a replace directive, which is what "插件与宿主共享 sshx" means.
//
// Contents:
//   - config.go    — the OpenSSH client-config parser (迁自 internal/sshconfig)
//   - dial.go      — SSHConfig/ProxyConfig, validate, the bastion dial chain,
//     auth methods, the SOCKS5 dialer (迁自 ssh.go/proxy.go)
//   - pty.go       — an interactive PTY session over an SSH client (W-T1: the
//     terminal channel's backend)
//
// Nothing here knows about the workbench protocol: the plugin wires sshx types
// into channel frames; the host wires them into database tunnels.
package sshx

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

// SSHConfig is the jump-host ("bastion") configuration saved with a connection.
// It is intentionally a plain struct: it round-trips through the same state file
// as the rest of ConnectionConfig, and the password/key passphrase are stored
// through the same secrets layer as the database password.
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

// Validate is the exported form: the workbench plugin validates a session's
// config before dialling, exactly like the host's connection dialog does.
func (c SSHConfig) Validate() error { return c.validate() }

// sshDialer is the transport a hop is reached through (direct, or via SOCKS5).
type sshDialer = func(ctx context.Context, network, addr string) (net.Conn, error)

// SSHPort is the config's port or the SSH default.
func SSHPort(c SSHConfig) int {
	if c.Port > 0 {
		return c.Port
	}
	return 22
}

// DialChain opens the SSH connection(s), bastion first when configured, and
// returns every client in the chain (outermost first) so callers can close them
// in reverse order.
//
// A two-hop chain is built the only way it can be: the second hop is dialled
// *through* the first hop's transport, so the inner connection never touches
// this machine's network stack.
func DialChain(cfg SSHConfig) ([]*ssh.Client, error) {
	dialerFor := func(c SSHConfig) (sshDialer, error) {
		if c.Proxy != nil && c.Proxy.Enabled {
			return Socks5Dialer(*c.Proxy)
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
	jumpTarget := net.JoinHostPort(cfg.Host, strconv.Itoa(SSHPort(cfg)))
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

// dialSSHOnce opens one SSH connection to cfg's own address.
func dialSSHOnce(cfg SSHConfig, dial sshDialer) (*ssh.Client, error) {
	return dialSSHOnceAt(cfg, dial, net.JoinHostPort(cfg.Host, strconv.Itoa(SSHPort(cfg))))
}

// dialSSHOnceAt opens one SSH connection whose transport is already positioned
// at target. The transport and the handshake target are separate because a
// chained hop dials a fixed address (the next hop) rather than through a
// per-address dialer.
func dialSSHOnceAt(cfg SSHConfig, dial sshDialer, target string) (*ssh.Client, error) {
	auth, err := AuthMethods(cfg)
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

// AuthMethods prefers key auth when a key file is configured and falls back
// to the password; both may be present (some bastions require 2FA-style pairs).
func AuthMethods(cfg SSHConfig) ([]ssh.AuthMethod, error) {
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

// CloseChain closes a chain innermost-first: closing a hop before the channel
// that rides on it turns a clean shutdown into a broken-pipe error in the logs.
func CloseChain(clients []*ssh.Client) {
	for i := len(clients) - 1; i >= 0; i-- {
		if clients[i] != nil {
			_ = clients[i].Close()
		}
	}
}

// Socks5Dialer builds a dialer for the configured proxy.
//
// W4-c: reaching the SSH host through a SOCKS5 proxy. This exists for the case
// where the jump host is on a network this machine cannot route to directly (a
// corporate SOCKS gateway, a bastion service). The proxy applies to the FIRST
// SSH connection only — the second hop rides the first hop's channel and needs
// no proxy of its own.
//
// Only SOCKS5 is supported, and things it cannot do are refused rather than
// silently downgraded: a proxy that needs HTTP CONNECT is a different protocol,
// and guessing would produce a "connection refused" the user cannot explain.
func Socks5Dialer(cfg ProxyConfig) (sshDialer, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("代理: 主机是必填项")
	}
	port := cfg.Port
	if port == 0 {
		port = 1080 // the SOCKS convention
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("代理: 端口无效: %d", port)
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", port))

	var auth *proxy.Auth
	if cfg.User != "" || cfg.Password != "" {
		auth = &proxy.Auth{User: cfg.User, Password: cfg.Password}
	}

	// forward is the SOCKS5 dialer; direct is the fallback dialer it uses for
	// addresses the proxy would not handle. RemoteDNS decides whether the
	// target name is sent to the proxy (ATYP=domain) or resolved here — the
	// whole point of a proxy is usually that this machine cannot resolve the
	// internal name, so it defaults to remote resolution.
	base := &net.Dialer{Timeout: 10e9}
	d, err := proxy.SOCKS5("tcp", addr, auth, base)
	if err != nil {
		return nil, fmt.Errorf("代理: %w", err)
	}
	ctxDialer, ok := d.(proxy.ContextDialer)
	if !ok {
		// x/net/proxy's SOCKS5 dialer implements ContextDialer; a future change
		// that dropped it would break cancellation, so say so instead of
		// falling back to an uncancellable dial.
		return nil, fmt.Errorf("代理: 该 SOCKS5 实现不支持上下文取消")
	}
	_ = cfg.RemoteDNS // x/net/proxy always resolves remotely; recorded for the UI
	return ctxDialer.DialContext, nil
}
