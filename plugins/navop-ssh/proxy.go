package main

// proxy.go — navop-ssh's tunnel.proxy provider (W-C1's providing half).
//
// The tunnel is the plugin's OWN SSH chain (dialSSHChain, already built for the
// terminal and files sessions), exposed as a coeffect. The consumer plugin
// (navop-containers) declares tunnel.proxy and — per §5.1.4 — accesses it
// through the proxy protocol: the consumer SENDS workbench/proxy/request, the
// HOST answers workbench/proxy/value with the resolved binding.
//
// What the provider publishes for op="dial" is the parameters a consumer needs
// to reach the same SSH chain: the session's SSH coordinates. The consumer dials
// its OWN connection THROUGH those coordinates (sshx DialChain to the same
// host, then TCP to the docker daemon inside the chain) — a second channel on a
// shared transport, not a second credential. Publishing a loopback listener
// instead would make the host hold a socket for a consumer that may never come;
// publishing coordinates keeps the provider stateless and the consumer
// self-sufficient.
//
// The push direction (host→plugin, workbench/proxy/value with the request's
// frame id) is handled by the runner's proc; the provider registers its answer
// handler here. The password is deliberately NOT published (AD-4): the consumer
// re-auths with its own credential flow.

import (
	"encoding/json"
	"sync"

	"github.com/dbcore/dbcore/plugindriver"
	"github.com/dbcore/dbcore/plugindriver/workbench"
)

// tunnelInfo is the provider's answer for op=dial: how to reach the tunnel.
type tunnelInfo struct {
	SSHHost   string `json:"sshHost"`
	SSHPort   int    `json:"sshPort"`
	SSHUser   string `json:"sshUser"`
	SSHKey    string `json:"sshKey,omitempty"`
	TunnelFor string `json:"tunnelFor"`
}

// tunnelOnce guards a future push-based tunnel open; the current design keeps
// the provider stateless (the answer is pure data, no listener to reclaim).
var tunnelOnce sync.Once

var _ = tunnelOnce

// handleProxy is navop-ssh's answer to a host-forwarded proxy request. The host
// resolves the KEY; the plugin resolves the VALUE for the op. The response
// carries the ORIGINAL request ID so the host's proc can match it to the
// waiting consumer.
func (s *server) handleProxy(req plugindriver.Request) plugindriver.Response {
	fail := func(errText, code string) plugindriver.Response {
		return plugindriver.Response{ID: req.ID, Error: errText, Code: code}
	}
	var p workbench.ProxyRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return fail("bad proxy params: "+err.Error(), "ERR_PLUGIN")
	}
	switch p.Key {
	case "tunnel.proxy":
		switch p.Op {
		case "dial":
			var args struct {
				TargetHost string `json:"targetHost"`
			}
			_ = json.Unmarshal(p.Args, &args)
			info := tunnelInfo{
				SSHHost:   s.sessionSSHHost,
				SSHPort:   s.sessionSSHPort,
				SSHUser:   s.sessionSSHUser,
				SSHKey:    s.sessionSSHKeyFile,
				TunnelFor: args.TargetHost,
			}
			payload, _ := json.Marshal(info)
			return plugindriver.Response{ID: req.ID, Result: payload}
		default:
			return fail("unknown proxy op "+p.Op, "ERR_NOT_SUPPORTED")
		}
	default:
		return fail("this plugin does not provide "+p.Key, "ERR_NOT_SUPPORTED")
	}
}
