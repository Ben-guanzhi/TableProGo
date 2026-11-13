package main

// sessions.go — the container actions, over the tunnel the proxy resolved.
//
// The docker daemon is reached as a PLAIN TCP dial INSIDE the SSH chain: sshx
// DialChain to the ssh host, then client.Dial to the daemon address the
// consumer asked for (127.0.0.1:2375 by convention). The HTTP requests are
// hand-rolled (GET /containers/json is one endpoint) rather than pulling a
// docker client — the plugin's dependency surface stays the SSH library it
// already builds against.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"tablepro-go/sshx"
)

// tunnelInfo is the proxy value's shape (navop-ssh's tunnel.proxy answer): how
// to reach the tunnel. Declared here because the two plugins share a PROTOCOL,
// not a package.
type tunnelInfo struct {
	SSHHost   string `json:"sshHost"`
	SSHPort   int    `json:"sshPort"`
	SSHUser   string `json:"sshUser"`
	SSHKey    string `json:"sshKey,omitempty"`
	TunnelFor string `json:"tunnelFor"`
}

// sessionOpenParams: where the SSH host is and which daemon address inside the
// chain to talk to. The tunnel coordinates come from the proxy flow — the host
// resolves tunnel.proxy against the consumer's committed view before this call
// is ever forwarded.
type sessionOpenParams struct {
	Tunnel     tunnelInfo `json:"tunnel"`
	Password   string     `json:"password"`
	DockerAddr string     `json:"dockerAddr,omitempty"`
}

func (s *server) sessionOpen(raw json.RawMessage) (any, string) {
	var p sessionOpenParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, "bad open params: " + err.Error()
	}
	dockerAddr := p.DockerAddr
	if dockerAddr == "" {
		dockerAddr = "127.0.0.1:2375"
	}
	cfg := sshx.SSHConfig{
		Enabled:  true,
		Host:     p.Tunnel.SSHHost,
		Port:     p.Tunnel.SSHPort,
		User:     p.Tunnel.SSHUser,
		Password: p.Password,
	}
	clients, err := sshx.DialChain(cfg)
	if err != nil {
		return nil, "tunnel dial: " + err.Error()
	}
	inner := clients[len(clients)-1]
	// A reachability probe is the open's contract: fail HERE (a clear message)
	// rather than at the first ps, where it would look like an empty list.
	if err := probeDaemon(inner, dockerAddr); err != nil {
		sshx.CloseChain(clients)
		return nil, err.Error()
	}
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("docker-%d", s.seq)
	s.sessions[id] = &dockerSession{clients: clients, dockerHost: dockerAddr}
	s.mu.Unlock()
	return map[string]string{"sessionId": id}, ""
}

// probeDaemon opens one HTTP connection to the daemon's /_ping.
func probeDaemon(inner *ssh.Client, addr string) error {
	conn, err := inner.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("容器守护进程 %s 不可达: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET /_ping HTTP/1.1\r\nHost: " + addr + "\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("容器守护进程 ping: %w", err)
	}
	resp, err := readHTTPResponse(conn, 4096)
	if err != nil {
		return fmt.Errorf("容器守护进程 ping: %w", err)
	}
	if !strings.Contains(string(resp), "200") {
		return fmt.Errorf("容器守护进程 ping 返回异常: %s", firstLine(string(resp)))
	}
	return nil
}

// containerPS lists containers through the daemon HTTP API, shaped as rows the
// host grid renders (the same "console action returns a query-shaped result"
// contract as the vector/MQ consoles).
func (s *server) containerPS(session string) (any, string) {
	s.mu.Lock()
	sess := s.sessions[session]
	s.mu.Unlock()
	if sess == nil {
		return nil, "unknown container session " + session
	}
	inner := sess.clients[len(sess.clients)-1]
	body, err := daemonHTTP(inner, sess.dockerHost, "/containers/json?all=1")
	if err != nil {
		return nil, err.Error()
	}
	var containers []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
		State string   `json:"State"`
	}
	if err := json.Unmarshal(body, &containers); err != nil {
		return nil, "parse containers: " + err.Error()
	}
	rows := make([]map[string]any, 0, len(containers))
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		rows = append(rows, map[string]any{
			"id":    c.ID[:12],
			"name":  name,
			"image": c.Image,
			"state": c.State,
		})
	}
	return map[string]any{"containers": rows}, ""
}

// containerExec frames a shell INSIDE one container as a terminal channel (the
// plan: "exec 复用 terminal.channels"). The exec channel's id is returned like
// a channel id; data/close ride the same channel-data methods the terminal
// uses, so the HOST needs no new plumbing.
func (s *server) containerExec(session string, raw json.RawMessage) (any, string) {
	var p struct {
		Container string `json:"container"`
	}
	_ = json.Unmarshal(raw, &p)
	s.mu.Lock()
	sess := s.sessions[session]
	s.mu.Unlock()
	if sess == nil {
		return nil, "unknown container session " + session
	}
	inner := sess.clients[len(sess.clients)-1]
	sessionSSH, err := inner.NewSession()
	if err != nil {
		return nil, "exec session: " + err.Error()
	}
	stdin, err := sessionSSH.StdinPipe()
	if err != nil {
		_ = sessionSSH.Close()
		return nil, "exec stdin: " + err.Error()
	}
	stdout, err := sessionSSH.StdoutPipe()
	if err != nil {
		_ = sessionSSH.Close()
		return nil, "exec stdout: " + err.Error()
	}
	if err := sessionSSH.Start("docker exec -i " + p.Container + " /bin/sh"); err != nil {
		_ = sessionSSH.Close()
		return nil, "docker exec: " + err.Error()
	}
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("cexec-%d", s.seq)
	shell := &containerShell{session: sessionSSH, stdinPipe: stdin}
	s.shells[id] = shell
	s.mu.Unlock()
	// Pump the shell's output into the buffer, exactly like the PTY pump.
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			shell.mu.Lock()
			shell.buf = append(shell.buf, sc.Bytes()...)
			shell.buf = append(shell.buf, '\n')
			shell.mu.Unlock()
		}
		shell.mu.Lock()
		shell.ended = true
		shell.mu.Unlock()
		_ = sessionSSH.Close()
	}()
	return map[string]string{"channelId": id}, ""
}

// kubectl runs one kubectl invocation ON the remote host over a PTY-less exec
// and returns the output (the plan's "kubectl 透传插件化": the backup.go
// host-side special case becomes a plugin capability).
func (s *server) kubectl(session, args string) (any, string) {
	s.mu.Lock()
	sess := s.sessions[session]
	s.mu.Unlock()
	if sess == nil {
		return nil, "unknown container session " + session
	}
	inner := sess.clients[len(sess.clients)-1]
	sessionSSH, err := inner.NewSession()
	if err != nil {
		return nil, "exec session: " + err.Error()
	}
	defer sessionSSH.Close()
	out, err := sessionSSH.CombinedOutput("kubectl " + args)
	if err != nil {
		return nil, "kubectl: " + err.Error() + ": " + string(out)
	}
	return map[string]any{"output": string(out)}, ""
}

// daemonHTTP performs one HTTP request to the daemon through the chain and
// returns the body. It is a hand-rolled GET: the docker API surface this plugin
// uses is one endpoint, and the client library would be the largest dependency
// in the binary for it.
func daemonHTTP(inner *ssh.Client, addr, path string) ([]byte, error) {
	conn, err := inner.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("容器守护进程 %s 不可达: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	req := "GET " + path + " HTTP/1.1\r\nHost: " + addr + "\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return nil, fmt.Errorf("daemon request: %w", err)
	}
	return readHTTPResponse(conn, 8*1024*1024)
}

// readHTTPResponse reads a full HTTP/1.1 response (headers + body) with a cap,
// splitting on the blank line. Chunked transfer encoding is reassembled — the
// docker API answers chunked for /containers/json.
func readHTTPResponse(conn io.Reader, cap int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(conn, int64(cap)))
	if err != nil {
		return nil, err
	}
	text := string(raw)
	idx := strings.Index(text, "\r\n\r\n")
	if idx < 0 {
		return nil, fmt.Errorf("malformed HTTP response")
	}
	head := text[:idx]
	body := text[idx+4:]
	if strings.Contains(strings.ToLower(head), "transfer-encoding: chunked") {
		body = dechunk(body)
	}
	return []byte(body), nil
}

// dechunk reassembles a chunked body.
func dechunk(body string) string {
	var out strings.Builder
	for {
		lineEnd := strings.Index(body, "\r\n")
		if lineEnd < 0 {
			return out.String()
		}
		sizeHex := strings.TrimSpace(body[:lineEnd])
		size, err := strconv.ParseInt(sizeHex, 16, 64)
		if err != nil || size <= 0 {
			return out.String()
		}
		start := lineEnd + 2
		if start+int(size) > len(body) {
			return out.String()
		}
		out.WriteString(body[start : start+int(size)])
		body = body[start+int(size):]
		body = strings.TrimPrefix(body, "\r\n")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// shellData writes keystrokes into one container shell and returns the output
// accumulated since the previous drain. This is the channel-data contract for
// the exec channel: the host polls, the shell's pump fills the buffer.
func (s *server) shellData(channelID, data string) (any, string) {
	s.mu.Lock()
	sh := s.shells[channelID]
	s.mu.Unlock()
	if sh == nil {
		return nil, "unknown container shell " + channelID
	}
	if data != "" {
		if _, err := io.WriteString(sh.stdinPipe, data); err != nil {
			return nil, "exec write: " + err.Error()
		}
	}
	sh.mu.Lock()
	out := string(sh.buf)
	sh.buf = sh.buf[:0]
	ended := sh.ended
	sh.mu.Unlock()
	return map[string]any{"output": out, "ended": ended}, ""
}

// closeShell drops one container shell (its session closes with it).
func (s *server) closeShell(channelID string) {
	s.mu.Lock()
	sh := s.shells[channelID]
	delete(s.shells, channelID)
	s.mu.Unlock()
	if sh == nil {
		return
	}
	_ = sh.stdinPipe.Close()
	_ = sh.session.Close()
}

// closeAllFor drops every handle that belongs to one container session (the
// plugin-side inverse of open).
func (s *server) closeAllFor(session string) {
	s.mu.Lock()
	sess := s.sessions[session]
	delete(s.sessions, session)
	for id, sh := range s.shells {
		_ = sh.session.Close()
		delete(s.shells, id)
	}
	s.mu.Unlock()
	if sess != nil {
		sshx.CloseChain(sess.clients)
	}
}

func (s *server) closeEverything() {
	s.mu.Lock()
	sessions := make([]*dockerSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.sessions = map[string]*dockerSession{}
	for _, sh := range s.shells {
		_ = sh.session.Close()
	}
	s.shells = map[string]*containerShell{}
	s.mu.Unlock()
	for _, sess := range sessions {
		sshx.CloseChain(sess.clients)
	}
}
