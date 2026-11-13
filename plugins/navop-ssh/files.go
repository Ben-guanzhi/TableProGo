package main

// files.go — W-F1: SFTP sessions framed as a files.session capability.
//
// The session lifecycle mirrors the terminal channels: the host asks for a
// "files-open" (dial + mount the SFTP subsystem), gets a generator-allocated id,
// and addresses every later action by that id. Closing drops the session AND its
// SSH chain.
//
// THE ACQUISITION DISCIPLINE (§6.1), operationalized by the upload shape:
// upload writes remotePath+".tmp" and the ACTION replies with the tmp name and
// the commit is a separate rename action. The host tracks BOTH as effect
// inverses: the tmp cleanup (a failed transfer's residue) and the rename
// (revertible to the tmp state). A direct write to the target would be an
// emission — half-written files on a dropped connection, unrevolvable.
//
// Path safety: every action's path is checked against the session's allowed
// roots (declared at open). This is the plan's "路径白名单 = 宿主在协议边界对
// files 键的隔离 realm (Σiso)" — the realm is the session, and the plugin
// refuses paths outside it. The HOST enforces the same rule again (defense in
// depth), but the plugin is where the path meets the filesystem.

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"
	"tablepro-go/sshx"
)

// filesOpenParams mirrors the terminal's open, plus the allowed roots: the
// realm Σiso assigns THIS session to. Empty roots = allow all (loopback dev);
// a real deployment declares the subtrees the session may touch.
type filesOpenParams struct {
	Host    string   `json:"host"`
	Port    int      `json:"port"`
	User    string   `json:"user"`
	Secret  string   `json:"secret,omitempty"` // AD-4 lend name
	KeyFile string   `json:"keyFile,omitempty"`
	Passphr string   `json:"passphrase,omitempty"`
	Roots   []string `json:"roots,omitempty"`
}

// fileSession is one SFTP session with its realm.
type fileSession struct {
	clients []*ssh.Client
	sftp    *sshx.SFTPSession
	roots   []string
}

// inRealm reports whether p stays inside one of the session's roots.
// A root "" means the whole tree (dev mode). Path traversal ("..") is resolved
// before the check — refusing "/a/../b" outside the realm while allowing it
// inside is the whole point of resolving first.
func (fs *fileSession) inRealm(p string) bool {
	if len(fs.roots) == 0 {
		return true
	}
	clean := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	for _, root := range fs.roots {
		rc := path.Clean(root)
		if rc == "" || rc == "." {
			return true
		}
		if clean == rc || strings.HasPrefix(clean, rc+"/") {
			return true
		}
	}
	return false
}

// realmErr builds the Σiso refusal (path outside the session's realm).
func realmErr(p string) (any, string) {
	return nil, fmt.Sprintf("路径 %s 超出本会话的白名单（隔离 realm Σiso）", p)
}

// filesAction is the whole file-action surface: one method, action-dispatched,
// session-addressed. The actions mirror sshx.SFTPSession 1:1 — the plugin is a
// protocol adapter, not a second file library.
func (s *server) filesAction(raw json.RawMessage) (any, string) {
	var head struct {
		ChannelID string `json:"channelId"`
		Action    string `json:"action"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, "bad files params: " + err.Error()
	}

	// Session lifecycle actions may create the session.
	switch head.Action {
	case "open":
		var p filesOpenParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, "bad open params: " + err.Error()
		}
		// AD-4: a password in the parameters is refused (see openChannel).
		var probe map[string]json.RawMessage
		_ = json.Unmarshal(raw, &probe)
		if _, leaked := probe["password"]; leaked {
			return nil, "AD-4: 密码必须经秘密借出（stdin 原始体），不得随参数传输"
		}
		var password string
		if p.Secret != "" {
			val, erase, ok := s.vault.Take(p.Secret)
			if !ok {
				return nil, "secret " + p.Secret + " 未被借出（lend 是一次性的，请重新借出）"
			}
			defer erase()
			password = string(val)
		}
		cfg := sshx.SSHConfig{
			Enabled: true, Host: p.Host, Port: p.Port, User: p.User,
			Password: password, KeyFile: p.KeyFile, Passphrase: p.Passphr,
		}
		if err := cfg.Validate(); err != nil {
			return nil, err.Error()
		}
		// Record the coordinates for tunnel.proxy (W-C1): a files session rides
		// the SAME chain kind a terminal does, so it is also a valid source of
		// the tunnel's coordinates. The password stays out of the record (AD-4).
		s.mu.Lock()
		s.sessionSSHHost = p.Host
		s.sessionSSHPort = p.Port
		s.sessionSSHUser = p.User
		s.sessionSSHKeyFile = p.KeyFile
		s.mu.Unlock()
		clients, err := sshx.DialChain(cfg)
		if err != nil {
			return nil, err.Error()
		}
		sftpSess, err := sshx.OpenSFTP(clients[len(clients)-1])
		if err != nil {
			sshx.CloseChain(clients)
			return nil, err.Error()
		}
		s.mu.Lock()
		s.seq++
		id := fmt.Sprintf("fs-%d", s.seq)
		s.files[id] = &fileSession{clients: clients, sftp: sftpSess, roots: p.Roots}
		s.mu.Unlock()
		return map[string]string{"channelId": id}, ""
	}

	s.mu.Lock()
	fs := s.files[head.ChannelID]
	s.mu.Unlock()
	if fs == nil {
		return nil, "unknown files session " + head.ChannelID
	}

	// Every session-scoped action declares its paths; the realm check runs
	// BEFORE the filesystem sees them.
	pathsOf := func() []string {
		var p struct {
			Path     string `json:"path"`
			To       string `json:"to"`
			LocalTmp string `json:"localTmp"`
		}
		_ = json.Unmarshal(raw, &p)
		var out []string
		if p.Path != "" {
			out = append(out, p.Path)
		}
		if p.To != "" {
			out = append(out, p.To)
		}
		return out
	}
	if paths := pathsOf(); len(paths) > 0 {
		for _, p := range paths {
			if !fs.inRealm(p) {
				return realmErr(p)
			}
		}
	}

	switch head.Action {
	case "list":
		entries, err := fs.sftp.List(pathOf(raw, "path"))
		if err != nil {
			return nil, err.Error()
		}
		return map[string]any{"entries": entries}, ""
	case "stat":
		st, err := fs.sftp.Stat(pathOf(raw, "path"))
		if err != nil {
			return nil, err.Error()
		}
		return map[string]any{"entry": st}, ""
	case "mkdir":
		if err := fs.sftp.Mkdir(pathOf(raw, "path")); err != nil {
			return nil, err.Error()
		}
		return map[string]bool{"ok": true}, ""
	case "remove":
		if err := fs.sftp.Remove(pathOf(raw, "path")); err != nil {
			return nil, err.Error()
		}
		return map[string]bool{"ok": true}, ""
	case "rename":
		var p struct {
			Path string `json:"path"`
			To   string `json:"to"`
		}
		_ = json.Unmarshal(raw, &p)
		if err := fs.sftp.Rename(p.Path, p.To); err != nil {
			return nil, err.Error()
		}
		return map[string]bool{"ok": true}, ""
	case "download":
		// Streams remotePath into the host-supplied localTmp file. The HOST
		// renames it into place (atomic on the local volume) — the plugin never
		// touches local state.
		var p struct {
			Path     string `json:"path"`
			LocalTmp string `json:"localTmp"`
		}
		_ = json.Unmarshal(raw, &p)
		n, err := fs.sftp.DownloadToTmp(p.Path, p.LocalTmp)
		if err != nil {
			return nil, err.Error()
		}
		return map[string]any{"path": p.LocalTmp, "size": n}, ""
	case "upload":
		// Two-step acquisition: stage to remotePath+".tmp", reply with the tmp
		// name. The host renames (commit) and tracks the tmp cleanup as the
		// effect inverse — a failed transfer's residue is reclaimable.
		var p struct {
			LocalTmp string `json:"localTmp"`
			Path     string `json:"path"`
		}
		_ = json.Unmarshal(raw, &p)
		tmp, n, err := fs.sftp.UploadToTmp(p.LocalTmp, p.Path)
		if err != nil {
			return nil, err.Error()
		}
		return map[string]any{"tmp": tmp, "size": n}, ""
	case "commit-upload":
		var p struct {
			Tmp  string `json:"tmp"`
			Path string `json:"path"`
		}
		_ = json.Unmarshal(raw, &p)
		if !fs.inRealm(p.Tmp) || !fs.inRealm(p.Path) {
			return realmErr(p.Tmp)
		}
		if err := fs.sftp.Rename(p.Tmp, p.Path); err != nil {
			return nil, err.Error()
		}
		return map[string]bool{"ok": true}, ""
	case "discard-upload":
		var p struct {
			Tmp string `json:"tmp"`
		}
		_ = json.Unmarshal(raw, &p)
		if !fs.inRealm(p.Tmp) {
			return realmErr(p.Tmp)
		}
		if err := fs.sftp.RemoveTmp(p.Tmp); err != nil {
			return nil, err.Error()
		}
		return map[string]bool{"ok": true}, ""
	default:
		return nil, "unknown files action " + head.Action
	}
}

// pathOf pulls one path field out of the raw params.
func pathOf(raw json.RawMessage, field string) string {
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	v, _ := p[field].(string)
	return v
}

// closeFileSession drops one files session and its SSH chain.
func (s *server) closeFileSession(id string) {
	s.mu.Lock()
	fs := s.files[id]
	delete(s.files, id)
	s.mu.Unlock()
	if fs == nil {
		return
	}
	_ = fs.sftp.Close()
	sshx.CloseChain(fs.clients)
}

// closeAllFiles tears down every files session (process shutdown).
func (s *server) closeAllFiles() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.files))
	for id := range s.files {
		ids = append(ids, id)
	}
	s.files = map[string]*fileSession{}
	s.mu.Unlock()
	for _, id := range ids {
		s.closeFileSession(id)
	}
}
