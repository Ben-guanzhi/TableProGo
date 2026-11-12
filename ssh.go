package main

// The SSH tunnel: a local port-forward through one or more jump hosts, used to
// reach database endpoints. W-T1 迁入：the SSH *capability* (config, dial chain,
// auth, SOCKS5, PTY) now lives in the sshx package, which the navop-ssh workbench
// plugin builds against too — one copy, not two forks. What remains here is the
// TUNNEL only (host-side, DB connections), plus type aliases that keep the saved
// connection JSON and the rest of the host compiling unchanged.

import (
	"fmt"
	"io"
	"net"
	"strconv"

	"tablepro-go/sshx"

	"golang.org/x/crypto/ssh"
)

// SSHConfig and ProxyConfig live in sshx now; these aliases keep the saved
// connection JSON and every reference in the host compiling unchanged.
type SSHConfig = sshx.SSHConfig
type ProxyConfig = sshx.ProxyConfig

// dialSSHChain and friends are sshx.DialChain now; keep the local names the
// tunnel and its tests use.
var dialSSHChain = sshx.DialChain

func closeClients(clients []*ssh.Client) { sshx.CloseChain(clients) }

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
	if err := cfg.Validate(); err != nil {
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
