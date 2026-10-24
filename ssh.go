package main

import (
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
	return nil
}

// sshTunnel is one local port-forward through a jump host: a loopback listener
// whose accepted connections are carried over an SSH "direct-tcpip" channel to
// the database endpoint.
type sshTunnel struct {
	client   *ssh.Client
	listener net.Listener
	remote   string
}

// openSSHTunnel connects to the jump host and starts forwarding to
// remoteHost:remotePort. It returns the loopback address the drivers should dial.
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

	auth, err := sshAuthMethods(cfg)
	if err != nil {
		return nil, "", 0, err
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(port)), &ssh.ClientConfig{
		User: cfg.User,
		Auth: auth,
		// A desktop client cannot ask the user to verify a fingerprint on every
		// connect, so the host key is accepted as-is (same trade-off as the
		// reference clients). Documented here rather than silently implied.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return nil, "", 0, fmt.Errorf("ssh: connect to %s: %w", net.JoinHostPort(cfg.Host, strconv.Itoa(port)), err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		return nil, "", 0, fmt.Errorf("ssh: local listener: %w", err)
	}
	t := &sshTunnel{
		client:   client,
		listener: listener,
		remote:   net.JoinHostPort(remoteHost, strconv.Itoa(remotePort)),
	}
	go t.forward()
	addr := listener.Addr().(*net.TCPAddr)
	return t, addr.IP.String(), addr.Port, nil
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
// to the remote database through the SSH connection.
func (t *sshTunnel) forward() {
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return
		}
		go t.pipe(local)
	}
}

func (t *sshTunnel) pipe(local net.Conn) {
	remote, err := t.client.Dial("tcp", t.remote)
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

// Close tears down the listener and the SSH connection (idempotent).
func (t *sshTunnel) Close() {
	if t == nil {
		return
	}
	if t.listener != nil {
		_ = t.listener.Close()
	}
	if t.client != nil {
		_ = t.client.Close()
	}
}
