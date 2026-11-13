// Command navop-desktop is the W-D1 workbench plugin: remote desktops.
//
// THE SPLIT (the plan's two bullets):
//
//	RDP (mstsc)  → EMISSION. Spawning a client is idΓ: there is no inverse for
//	               "a window appeared". What IS compensatable is the .rdp file
//	               the HOST writes to configure it — the host tracks that file's
//	               deletion as the effect inverse (the plan's "补偿：.rdp 临时
//	               文件清理登记为效应逆"). The plugin only spawns; it never owns
//	               local state.
//	VNC          → ACQUISITION. The plugin dials the VNC server and RAW FRAMES
//	               ride the channel (workbench/channel/data), so the host can
//	               pump an embedded viewer through it. Closing the channel closes
//	               the socket; the desktop.session row is the host's.
//	serial/telnet → belong to the ssh tail (navop-ssh), not here — the plan says
//	               so and this plugin refuses them rather than half-supporting
//	               them.
//
// CREDENTIALS: RDP's password is never written into the .rdp file (mstsc prompts).
// That is a deliberate AD-4-adjacent choice: the file we create is not a secret
// carrier, so the inverse is a plain delete.

package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/dbcore/dbcore/plugindriver"
	"github.com/dbcore/dbcore/plugindriver/workbench"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    "navop-desktop",
		Name:    "navop-desktop 远程桌面",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	srv := &server{channels: map[string]*vncSession{}}
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

// vncSession is one VNC socket relayed through a channel.
type vncSession struct {
	conn net.Conn
}

type server struct {
	mu       sync.Mutex
	seq      uint64
	channels map[string]*vncSession
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
			PluginID:   "navop-desktop",
			ProviderID: "navop-desktop-" + strconv.FormatInt(time.Now().UnixNano(), 36),
			Depends:    []workbench.Capability{{Key: "connections.store", Commutative: true}},
			Provides:   []workbench.Capability{{Key: "desktop.session", Commutative: true}},
		}, "", "")
	case workbench.MethodEffect:
		var p struct {
			Stage int `json:"stage"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.Stage == 0 {
			payload, _ := json.Marshal(map[string]string{"label": "远程桌面", "icon": "monitor"})
			send(workbench.EffectFrame{
				Tag: "navop-desktop/tab", Key: "nav.nodes", Op: "insert",
				Payload: payload, Inverse: "remove", Boundary: workbench.BoundaryAcquisition,
			}, "", "")
			return
		}
		send(workbench.EffectFrame{Done: true}, "", "")
	case workbench.MethodChannelOpen:
		var p struct {
			Kind string `json:"kind"` // rdp | vnc
			Host string `json:"host"`
			Port int    `json:"port"`
			User string `json:"user,omitempty"`
			File string `json:"file,omitempty"` // rdp: the .rdp path the host wrote
		}
		_ = json.Unmarshal(req.Params, &p)
		switch p.Kind {
		case "rdp":
			if errText := s.launchRDP(p.File, p.Host, p.Port); errText != "" {
				send(nil, errText, "ERR_PLUGIN")
				return
			}
			send(map[string]bool{"launched": true}, "", "")
		case "vnc":
			id, errText := s.openVNC(p.Host, p.Port)
			if errText != "" {
				send(nil, errText, "ERR_PLUGIN")
				return
			}
			send(map[string]string{"channelId": id}, "", "")
		default:
			// The plan puts serial/telnet in the ssh tail; refuse rather than
			// half-support a transport this plugin does not implement.
			send(nil, "navop-desktop 只提供 rdp/vnc；串口与 telnet 属于 navop-ssh 的尾巴", "ERR_NOT_SUPPORTED")
		}
	case workbench.MethodChannelData:
		var p struct {
			ChannelID string `json:"channelId"`
			Data      string `json:"data"` // base64: VNC frames are binary
			MaxBytes  int    `json:"maxBytes,omitempty"`
		}
		_ = json.Unmarshal(req.Params, &p)
		reply, ended, errText := s.relayVNC(p.ChannelID, p.Data, p.MaxBytes)
		if errText != "" {
			send(nil, errText, "ERR_PLUGIN")
			return
		}
		send(map[string]any{
			"data":  base64.StdEncoding.EncodeToString(reply),
			"ended": ended,
		}, "", "")
	case workbench.MethodChannelClose:
		var p struct {
			ChannelID string `json:"channelId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.closeVNC(p.ChannelID)
		send(map[string]bool{"ok": true}, "", "")
	case plugindriver.MethodClose:
		send(nil, "", "")
		s.closeAll()
		os.Exit(0)
	default:
		send(nil, "unknown method "+req.Method, "ERR_NOT_SUPPORTED")
	}
}

// launcherEnv is the TEST seam: point it at a stub to prove the spawn without
// opening a window. Production uses mstsc.
const launcherEnv = "NAVOP_DESKTOP_LAUNCHER"

// launchRDP spawns the RDP client on the .rdp file the HOST wrote. This is the
// EMISSION: the window's appearance has no inverse. The file's cleanup is the
// host's compensating inverse, tracked there.
func (s *server) launchRDP(file, host string, port int) string {
	if file == "" {
		return "RDP 需要宿主写入的 .rdp 文件路径（发射后无法撤回，只补偿文件清理）"
	}
	exe := os.Getenv(launcherEnv)
	if exe == "" {
		exe = "mstsc.exe"
	}
	args := []string{file}
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("拉起 RDP 客户端失败（%s，目标 %s:%d）: %v", exe, host, port, err)
	}
	// The child is not waited on: mstsc is a GUI the operator closes. Reaping it in
	// the background keeps the plugin from accumulating zombies.
	go func() { _ = cmd.Wait() }()
	return ""
}

// openVNC dials the VNC server and returns a channel id.
func (s *server) openVNC(host string, port int) (string, string) {
	if host == "" || port <= 0 {
		return "", "VNC 需要 host 与 port"
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 10*time.Second)
	if err != nil {
		return "", fmt.Sprintf("VNC 服务器 %s:%d 不可达: %v", host, port, err)
	}
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("vnc-%d", s.seq)
	s.channels[id] = &vncSession{conn: conn}
	s.mu.Unlock()
	return id, ""
}

// relayVNC writes the host's bytes to the server and returns whatever the server
// has to say right now (non-blocking poll: the channel contract is request/
// response, and a VNC client drives the pace).
func (s *server) relayVNC(id, b64data string, maxBytes int) ([]byte, bool, string) {
	s.mu.Lock()
	sess := s.channels[id]
	s.mu.Unlock()
	if sess == nil {
		return nil, true, "unknown VNC channel " + id
	}
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	if b64data != "" {
		raw, err := base64.StdEncoding.DecodeString(b64data)
		if err != nil {
			return nil, false, "bad base64 payload: " + err.Error()
		}
		if _, err := sess.conn.Write(raw); err != nil {
			return nil, true, "VNC 写入: " + err.Error()
		}
	}
	// Poll for a reply without blocking the channel contract.
	_ = sess.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, maxBytes)
	n, err := sess.conn.Read(buf)
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return nil, false, "" // nothing pending: a valid empty answer
		}
		return nil, true, ""
	}
	return buf[:n], false, ""
}

func (s *server) closeVNC(id string) {
	s.mu.Lock()
	sess := s.channels[id]
	delete(s.channels, id)
	s.mu.Unlock()
	if sess != nil {
		_ = sess.conn.Close()
	}
}

func (s *server) closeAll() {
	s.mu.Lock()
	all := make([]*vncSession, 0, len(s.channels))
	for _, sess := range s.channels {
		all = append(all, sess)
	}
	s.channels = map[string]*vncSession{}
	s.mu.Unlock()
	for _, sess := range all {
		_ = sess.conn.Close()
	}
}
