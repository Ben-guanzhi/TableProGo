// Command navop-containers is the W-C1 workbench plugin: container sessions
// (docker over the SSH tunnel) and a kubectl passthrough.
//
// THE DEPENDENCY (the wave's whole point): this plugin DECLARES tunnel.proxy
// and provides container.session. Per Def 53 / AD-2, its fiber stays INACTIVE
// until navop-ssh is loaded and ACTIVE — the host's target view is ⊥ while
// σ ⊭ d, and the UI shows "此能力需要安装 navop-ssh", not a bare error. When
// the provider dies, this fiber deactivates FIRST (Thm 70: the consumer's
// teardown runs while it can still read the provider's key), and a fresh
// provider uid re-activates it (§5.1.3's inertial reload).
//
// HOW THE TUNNEL IS USED (the provider half is navop-ssh/proxy.go): this plugin
// sends workbench/proxy/request {key:"tunnel.proxy", op:"dial"} through its own
// process pipes; the HOST resolves the key against the committed view (σ ⊧ d
// plus confinement), forwards the request to the PROVIDER plugin, and pushes
// the answer back as workbench/proxy/value. The value is the SSH coordinates;
// this plugin dials the docker daemon THROUGH them (sshx.DialChain to the ssh
// host, then TCP to the daemon inside the chain) — a second channel on the
// shared transport, no second credential.
//
// THE ACTIONS (all framed as container.session capabilities):
//   ps        docker /containers/json through the tunnel (structured rows)
//   exec      start a shell INSIDE a container — reuses terminal.channels by
//             framing the container's shell as a terminal channel (the plan's
//             "exec 复用 terminal.channels")
//   kubectl   passthrough: run kubectl ON the remote host over a PTY channel
//             and return the output (the plan's "kubectl 透传插件化" — the
//             backup.go host-side special case becomes a plugin capability)

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/dbcore/dbcore/plugindriver"
	"github.com/dbcore/dbcore/plugindriver/workbench"
	"golang.org/x/crypto/ssh"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    "navop-containers",
		Name:    "navop-containers 容器",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	srv := &server{sessions: map[string]*dockerSession{}, shells: map[string]*containerShell{}}
	in := bufio.NewReader(os.Stdin)
	var wmu sync.Mutex
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var req plugindriver.Request
			if json.Unmarshal(line, &req) == nil {
				go func(req plugindriver.Request) { srv.handle(out, &wmu, req) }(req)
			}
		}
		if err != nil {
			return
		}
	}
}

// server holds the docker sessions and container shells. Deliberately stateless
// about the tunnel: every dial goes through a fresh proxy request, so a stale
// tunnel value cannot outlive the provider that published it (§5.1.3: the
// target view is keyed by provider UIDS).
type server struct {
	mu       sync.Mutex
	seq      uint64
	sessions map[string]*dockerSession
	shells   map[string]*containerShell
}

// dockerSession is one container daemon handle: the SSH chain it rides on.
type dockerSession struct {
	clients []*ssh.Client
	// dockerHost is the daemon address INSIDE the chain (127.0.0.1:2375 on the
	// remote host, or wherever the consumer pointed the dial).
	dockerHost string
}

// containerShell is one exec'd shell inside a container, framed as a terminal
// channel (the plan: "exec 复用 terminal.channels").
type containerShell struct {
	session   *ssh.Session
	stdinPipe io.WriteCloser
	buf       []byte
	mu        sync.Mutex
	ended     bool
}

func (s *server) handle(out *bufio.Writer, wmu *sync.Mutex, req plugindriver.Request) {
	send := func(result any, errText, code string) {
		payload, _ := json.Marshal(result)
		resp := plugindriver.Response{ID: req.ID, Result: payload, Error: errText, Code: code}
		frame, _ := json.Marshal(resp)
		wmu.Lock()
		fmt.Fprintf(out, "%s\n", frame)
		out.Flush()
		wmu.Unlock()
	}

	switch req.Method {
	case plugindriver.MethodPing:
		send("pong", "", "")
	case workbench.MethodHello:
		send(workbench.Banner{
			Stream:     workbench.StreamFrameV1,
			PluginID:   "navop-containers",
			ProviderID: "navop-containers-" + strconv.FormatInt(time.Now().UnixNano(), 36),
			Depends: []workbench.Capability{
				{Key: "tunnel.proxy", Commutative: true},
				{Key: "connections.store", Commutative: true},
			},
			Provides: []workbench.Capability{
				{Key: "container.session", Commutative: true},
			},
		}, "", "")
	case workbench.MethodEffect:
		// The load iterator is ONE stage: register the 容器 nav node. Its
		// existence in the host's nav tree IS the activation proof.
		var p struct {
			Stage int `json:"stage"`
		}
		_ = json.Unmarshal(req.Params, &p)
		var frame workbench.EffectFrame
		if p.Stage == 0 {
			payload, _ := json.Marshal(map[string]string{"label": "容器", "icon": "box"})
			frame = workbench.EffectFrame{
				Tag: "navop-containers/tab", Key: "nav.nodes", Op: "insert",
				Payload: payload, Inverse: "remove", Boundary: workbench.BoundaryAcquisition,
			}
		} else {
			frame = workbench.EffectFrame{Done: true}
		}
		send(frame, "", "")
	case workbench.MethodProxyRequest:
		// W-C1: a CONSUMER (the test drives this directly) asks us to proxy —
		// we do not provide tunnel.proxy, so refuse honestly.
		resp := plugindriver.Response{ID: req.ID, Error: "this plugin does not provide tunnel.proxy", Code: "ERR_NOT_SUPPORTED"}
		frame, _ := json.Marshal(resp)
		wmu.Lock()
		fmt.Fprintf(out, "%s\n", frame)
		out.Flush()
		wmu.Unlock()
	case workbench.MethodChannelOpen:
		// container.session open: resolve the tunnel, dial the daemon INSIDE the
		// chain, verify reachability. The tunnel resolution is the proxy flow —
		// which the HOST mediates — so a real open only succeeds when the
		// dependency is live.
		res, errText := s.sessionOpen(req.Params)
		if errText != "" {
			send(nil, errText, "ERR_PLUGIN")
			return
		}
		send(res, "", "")
	case workbench.MethodChannelData:
		var p struct {
			Action  string          `json:"action"`
			Session string          `json:"session"`
			Args    json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal(req.Params, &p)
		switch p.Action {
		case "ps":
			res, errText := s.containerPS(p.Session)
			if errText != "" {
				send(nil, errText, "ERR_PLUGIN")
				return
			}
			send(res, "", "")
		case "exec":
			res, errText := s.containerExec(p.Session, p.Args)
			if errText != "" {
				send(nil, errText, "ERR_PLUGIN")
				return
			}
			send(res, "", "")
		case "kubectl":
			res, errText := s.kubectl(p.Session, string(p.Args))
			if errText != "" {
				send(nil, errText, "ERR_PLUGIN")
				return
			}
			send(res, "", "")
		case "exec-data":
			// The container shell rides terminal.channels: write the keystrokes
			// the host sends and answer with whatever output accumulated since
			// the last drain (the same read-and-clear contract the PTY channels
			// use for workbench/channel/data).
			var d struct {
				ChannelID string `json:"channelId"`
				Data      string `json:"data"`
			}
			_ = json.Unmarshal(req.Params, &d)
			res, errText := s.shellData(d.ChannelID, d.Data)
			if errText != "" {
				send(nil, errText, "ERR_PLUGIN")
				return
			}
			send(res, "", "")
		case "exec-close":
			var d struct {
				ChannelID string `json:"channelId"`
			}
			_ = json.Unmarshal(req.Params, &d)
			s.closeShell(d.ChannelID)
			send(map[string]bool{"ok": true}, "", "")
		default:
			send(nil, "unknown container action "+p.Action, "ERR_NOT_SUPPORTED")
		}
	case workbench.MethodChannelClose:
		var p struct {
			Session string `json:"session"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.closeAllFor(p.Session)
		send(map[string]bool{"ok": true}, "", "")
	case plugindriver.MethodClose:
		send(nil, "", "")
		s.closeEverything()
		os.Exit(0)
	default:
		send(nil, "unknown method "+req.Method, "ERR_NOT_SUPPORTED")
	}
}
