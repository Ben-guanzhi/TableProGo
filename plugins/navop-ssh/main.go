// Command navop-ssh is the W-T1 workbench plugin: an SSH terminal + files
// provider (W-F1 加 files.session)。
//
// It speaks the workbench protocol (plugindriver framing + workbench methods) on
// top of the banner, exactly the shape P0 defined:
//
//	hello    → the component interface (d, p): it PROVIDES terminal.channels,
//	           nav.nodes and files.session (witnessed commutative — tables with
//	           unique tags) and DECLARES connections.store (the host's connection
//	           archive). Satisfaction σ ⊧ d is decidable host-side, so the fiber
//	           stays Inactive until the host provides the key.
//	effect   → the load iterator's stages, one EffectFrame each: stage 0
//	           registers the 终端 nav node, stage 1 opens the channel pool, then
//	           Done. Every frame carries an inverse WITNESS ("remove") — the
//	           host owns the actual disposer.
//	channel  → PTY sessions: open dials the SSH host (sshx.DialChain) and starts
//	           a shell (sshx.OpenPTY); data writes keystrokes and reads output;
//	           resize/close as named. Each open channel gets a generator-
//	           allocated id — NOT a "lowest available descriptor" (§6.3 anti-
//	           pattern), so two channel opens commute and either can be
//	           withdrawn while the other stands (Thm 43/47).
//	files    → SFTP sessions over the SAME SSH chain (W-F1): a files channel
//	           opens an SFTP subsystem; the file actions (list/stat/mkdir/
//	           rename/upload/download) are framed over it. Upload lands as
//	           .tmp + rename (the acquisition discipline — §6.1), so a failed
//	           transfer leaves only a .tmp residue whose cleanup is the effect
//	           inverse the host tracks.
//
// KILL/RECOVER (the W-T1 acceptance): the host can kill this process at any
// moment. Death severs the pipes; the host's runner notices, retires the fiber,
// and its accumulator reclaims the nav node and the channel pool — the plugin
// holds NO host state, so recovery is a fresh fiber re-running the same load.
// A terminal tab being "reconnectable" is therefore a COROLLARY of the
// architecture (AD-2), not a patched special case.
//
// AD-4 obligation NOT yet discharged (recorded honestly): the SSH password
// currently arrives inside the channel-open parameters, i.e. over a JSON frame.
// The plan's secret discipline (lend over stdin with an erasing inverse) is a
// required follow-up before this plugin is exposed beyond loopback.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/dbcore/dbcore/plugindriver"
	"github.com/dbcore/dbcore/plugindriver/workbench"
	"golang.org/x/crypto/ssh"
	"tablepro-go/sshx"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    "navop-ssh",
		Name:    "navop-ssh 终端",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	srv := &server{channels: map[string]*channelHandle{}, files: map[string]*fileSession{}}
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

// server holds the plugin's live PTY channels and SFTP sessions. It deliberately
// holds NO host state: the nav node and the channel pool live in the HOST's
// tables, tracked with inverses — which is exactly why killing this process
// loses nothing the host cannot rebuild by re-running the same load on a fresh
// fiber.
type server struct {
	mu       sync.Mutex
	seq      uint64
	channels map[string]*channelHandle
	files    map[string]*fileSession // files.session bindings (W-F1)
	// sessionSSH* records the coordinates the LAST open used, so tunnel.proxy's
	// answer (W-C1) tells the consumer how to reach the same chain. The
	// password is deliberately NOT in this struct: the consumer re-auths with
	// its own credential flow (AD-4's stdin-lend is the recorded follow-up).
	sessionSSHHost    string
	sessionSSHPort    int
	sessionSSHUser    string
	sessionSSHKeyFile string
}

// channelHandle is one open PTY. The pump goroutine fills the buffer from the
// PTY's stdout; a data request drains it. A plain mutex-protected buffer is
// enough for W-T1's interactive pacing — the terminal is human-speed.
type channelHandle struct {
	mu    sync.Mutex
	buf   []byte
	ended bool

	pty     *sshx.PTYSession
	clients []*ssh.Client
	cols    uint16
	rows    uint16
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
	case workbench.MethodProxyRequest:
		// W-C1: the consumer's coeffect access, answered as a PUSH value frame
		// (the host forwards it to the requesting plugin). handleProxy resolves
		// the op against the session's recorded SSH coordinates.
		resp := s.handleProxy(req)
		frame, _ := json.Marshal(resp)
		wmu.Lock()
		fmt.Fprintf(out, "%s\n", frame)
		out.Flush()
		wmu.Unlock()
	case workbench.MethodHello:
		send(workbench.Banner{
			Stream:     workbench.StreamFrameV1,
			PluginID:   "navop-ssh",
			ProviderID: "navop-ssh-" + strconv.FormatInt(time.Now().UnixNano(), 36),
			Depends:    []workbench.Capability{{Key: "connections.store", Commutative: true}},
			Provides: []workbench.Capability{
				{Key: "terminal.channels", Commutative: true},
				{Key: "files.session", Commutative: true},
				{Key: "tunnel.proxy", Commutative: true},
			},
			Secrets: []string{"ssh.password"},
		}, "", "")
	case workbench.MethodEffect:
		var p struct {
			Stage int `json:"stage"`
		}
		_ = json.Unmarshal(req.Params, &p)
		send(effectFrame(p.Stage), "", "")
	case workbench.MethodChannelOpen:
		id, errText := s.openChannel(req.Params)
		if errText != "" {
			send(nil, errText, "ERR_PLUGIN")
			return
		}
		send(map[string]string{"channelId": id}, "", "")
	case workbench.MethodChannelData:
		res, errText := s.channelData(req.Params)
		if errText != "" {
			send(nil, errText, "ERR_PLUGIN")
			return
		}
		send(res, "", "")
	case workbench.MethodChannelSize:
		if errText := s.channelResize(req.Params); errText != "" {
			send(nil, errText, "ERR_PLUGIN")
			return
		}
		send(map[string]bool{"ok": true}, "", "")
	case workbench.MethodChannelClose:
		if errText := s.channelClose(req.Params); errText != "" {
			send(nil, errText, "ERR_PLUGIN")
			return
		}
		send(map[string]bool{"ok": true}, "", "")
	case workbench.MethodFilesAction:
		res, errText := s.filesAction(req.Params)
		if errText != "" {
			send(nil, errText, "ERR_PLUGIN")
			return
		}
		send(res, "", "")
	case plugindriver.MethodClose:
		send(nil, "", "")
		s.closeAll()
		s.closeAllFiles()
		os.Exit(0)
	default:
		send(nil, "unknown method "+req.Method, "ERR_NOT_SUPPORTED")
	}
}

// effectFrame is the load iterator's stage table (Def 17): two acquisition
// effects, then Done. The tags are STABLE across restarts — that is what makes
// the recovered fiber's surface observationally equal to the one it replaced
// (Thm 80: "its dynamic history leaves no trace").
func effectFrame(stage int) workbench.EffectFrame {
	switch stage {
	case 0:
		payload, _ := json.Marshal(map[string]string{"label": "终端", "icon": "terminal"})
		return workbench.EffectFrame{
			Tag: "navop-ssh/terminal-tab", Key: "nav.nodes", Op: "insert",
			Payload: payload, Inverse: "remove", Boundary: workbench.BoundaryAcquisition,
		}
	case 1:
		payload, _ := json.Marshal(map[string]string{"kind": "ssh-pool"})
		return workbench.EffectFrame{
			Tag: "navop-ssh/channel-pool", Key: "terminal.channels", Op: "insert",
			Payload: payload, Inverse: "remove", Boundary: workbench.BoundaryAcquisition,
		}
	default:
		return workbench.EffectFrame{Done: true}
	}
}

// openParams is what the host sends to start one terminal. The SSH password
// rides here for now — the AD-4 stdin-lend path is a recorded follow-up (see the
// package comment).
type openParams struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password"`
	KeyFile    string `json:"keyFile,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Cols       uint16 `json:"cols"`
	Rows       uint16 `json:"rows"`
}

func (s *server) openChannel(raw json.RawMessage) (string, string) {
	var p openParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", "bad open params: " + err.Error()
	}
	cfg := sshx.SSHConfig{
		Enabled: true, Host: p.Host, Port: p.Port, User: p.User,
		Password: p.Password, KeyFile: p.KeyFile, Passphrase: p.Passphrase,
	}
	if err := cfg.Validate(); err != nil {
		return "", err.Error()
	}
	// Record the session's SSH coordinates for tunnel.proxy (W-C1): the provider
	// answer tells the consumer how to reach the SAME chain. The password stays
	// out of the record on purpose (AD-4).
	s.mu.Lock()
	s.sessionSSHHost = p.Host
	s.sessionSSHPort = p.Port
	s.sessionSSHUser = p.User
	s.sessionSSHKeyFile = p.KeyFile
	s.mu.Unlock()
	clients, err := sshx.DialChain(cfg)
	if err != nil {
		return "", err.Error()
	}
	inner := clients[len(clients)-1]
	pty, err := sshx.OpenPTY(inner, p.Cols, p.Rows)
	if err != nil {
		sshx.CloseChain(clients)
		return "", err.Error()
	}

	// Generator-allocated id: an atomic counter on the server, never a
	// "lowest available descriptor".
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("ch-%d", s.seq)
	h := &channelHandle{
		pty:     pty,
		clients: clients,
		cols:    p.Cols,
		rows:    p.Rows,
	}
	s.channels[id] = h
	s.mu.Unlock()

	// Pump the PTY's output into the buffer; when the session ends, mark it so a
	// later data request reports the close instead of hanging.
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				h.mu.Lock()
				h.buf = append(h.buf, buf[:n]...)
				h.mu.Unlock()
			}
			if err != nil {
				h.mu.Lock()
				h.ended = true
				h.mu.Unlock()
				return
			}
		}
	}()
	return id, ""
}

type dataResult struct {
	ChannelID string `json:"channelId"`
	Output    string `json:"output"`
	Ended     bool   `json:"ended"`
}

func (s *server) channelData(raw json.RawMessage) (any, string) {
	var p struct {
		ChannelID string `json:"channelId"`
		Input     string `json:"input,omitempty"`
		MaxBytes  int    `json:"maxBytes,omitempty"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, "bad data params: " + err.Error()
	}
	s.mu.Lock()
	h := s.channels[p.ChannelID]
	s.mu.Unlock()
	if h == nil {
		return nil, "unknown channel " + p.ChannelID
	}
	if p.Input != "" {
		if _, err := h.pty.Write([]byte(p.Input)); err != nil {
			return nil, "write pty: " + err.Error()
		}
	}
	max := p.MaxBytes
	if max <= 0 {
		max = 8192
	}
	// Give the shell a beat to answer an interactive command before draining.
	time.Sleep(80 * time.Millisecond)
	h.mu.Lock()
	n := len(h.buf)
	if n > max {
		n = max
	}
	out := string(h.buf[:n])
	h.buf = h.buf[n:]
	ended := h.ended
	h.mu.Unlock()
	return dataResult{ChannelID: p.ChannelID, Output: out, Ended: ended}, ""
}

func (s *server) channelResize(raw json.RawMessage) string {
	var p struct {
		ChannelID string `json:"channelId"`
		Cols      uint16 `json:"cols"`
		Rows      uint16 `json:"rows"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "bad resize params: " + err.Error()
	}
	s.mu.Lock()
	h := s.channels[p.ChannelID]
	s.mu.Unlock()
	if h == nil {
		return "unknown channel " + p.ChannelID
	}
	if err := h.pty.Resize(p.Cols, p.Rows); err != nil {
		return "resize: " + err.Error()
	}
	return ""
}

func (s *server) channelClose(raw json.RawMessage) string {
	var p struct {
		ChannelID string `json:"channelId"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "bad close params: " + err.Error()
	}
	s.mu.Lock()
	h := s.channels[p.ChannelID]
	delete(s.channels, p.ChannelID)
	s.mu.Unlock()
	if h == nil {
		return "unknown channel " + p.ChannelID
	}
	_ = h.pty.Close()
	sshx.CloseChain(h.clients)
	return ""
}

func (s *server) closeAll() {
	s.mu.Lock()
	handles := make([]*channelHandle, 0, len(s.channels))
	for _, h := range s.channels {
		handles = append(handles, h)
	}
	s.channels = map[string]*channelHandle{}
	s.mu.Unlock()
	for _, h := range handles {
		_ = h.pty.Close()
		sshx.CloseChain(h.clients)
	}
}
