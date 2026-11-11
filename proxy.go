package main

import (
	"fmt"
	"net"

	"golang.org/x/net/proxy"
)

// W4-c: reaching the SSH host through a SOCKS5 proxy.
//
// This exists for the case where the jump host is on a network this machine
// cannot route to directly (a corporate SOCKS gateway, a bastion service). The
// proxy applies to the FIRST SSH connection only — the second hop rides the
// first hop's channel and needs no proxy of its own.
//
// Only SOCKS5 is supported, and things it cannot do are refused rather than
// silently downgraded: a proxy that needs HTTP CONNECT is a different protocol,
// and guessing would produce a "connection refused" the user cannot explain.

// socks5Dialer builds a dialer for the configured proxy.
func socks5Dialer(cfg ProxyConfig) (sshDialer, error) {
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
