package workbenchhost

// desktop.go — W-D1's host half: RDP as an emission with a compensating inverse,
// VNC as a channel-relayed acquisition.
//
// THE COMPENSATION (the plan's exact words: "补偿：.rdp 临时文件清理登记为效应逆"):
//
//	Spawning mstsc is idΓ — a window appeared and no inverse can un-appear it. What
//	the host CAN do is own the configuration file it wrote: StartRDP writes a 0600
//	.rdp temp, tracks its DELETION on the fiber's accumulator, and only then asks
//	the plugin to spawn. Unloading the fiber (or CloseDesktopSession) removes the
//	file. The emission stays an emission; the compensation is real and tracked, and
//	the UI says "已拉起，不可撤回" rather than pretending otherwise.
//
// THE PASSWORD RULE: the .rdp file never contains the account password (mstsc
// prompts). That keeps the file out of the secret path entirely — its inverse is a
// plain delete, not an erasure.
//
// VNC is an ACQUISITION: the plugin dials the server, and raw frames ride
// workbench/channel/data (base64 over the JSON envelope, since VNC bytes are
// binary). Closing the channel closes the socket; the desktop.session row is the
// host's and is reclaimed on unload (Cor 69).

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dbcore/dbcore/plugindriver/workbench"
	"tablepro-go/internal/ctxcore"
)

// RDPStart is what the API/UI receives: where the file went and whether the
// client was spawned. It never contains a credential.
type RDPStart struct {
	File      string `json:"file"`
	Session   string `json:"session"`
	Launched  bool   `json:"launched"`
	Target    string `json:"target"`
	Notice    string `json:"notice"`
	Extension bool   `json:"extension"`
}

// rdpFileContent renders the minimal .rdp an operator needs. Deliberately no
// password field: mstsc prompts, and a file we own must not become a secret store.
func rdpFileContent(host string, port int, user string) string {
	addr := host
	if port > 0 && port != 3389 {
		addr = fmt.Sprintf("%s:%d", host, port)
	}
	var b strings.Builder
	b.WriteString("full address:s:" + addr + "\r\n")
	if user != "" {
		b.WriteString("username:s:" + user + "\r\n")
	}
	b.WriteString("prompt for credentials:i:1\r\n")
	b.WriteString("authentication level:i:2\r\n")
	b.WriteString("screen mode id:i:1\r\n")
	b.WriteString("use multimon:i:0\r\n")
	b.WriteString("audiomode:i:0\r\n")
	b.WriteString("redirectclipboard:i:1\r\n")
	return b.String()
}

// desktopDirOverride is the test seam for where .rdp files land.
var desktopDirOverride string

func desktopTempDir() string {
	if desktopDirOverride != "" {
		return desktopDirOverride
	}
	return filepath.Join(userDataDirOrDefault("desktop"), "sessions")
}

// StartRDP writes the .rdp file, tracks its removal as the compensating inverse,
// then asks the plugin to spawn the client. The order matters: the inverse must be
// registered BEFORE the spawn, or a crash between the two would leave an
// untracked file behind.
func (r *Runner) StartRDP(host string, port int, user string) (RDPStart, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return RDPStart{}, fmt.Errorf("%s: the desktop plugin fiber is not active", workbench.CodeInactive)
	}
	if strings.TrimSpace(host) == "" {
		return RDPStart{}, fmt.Errorf("RDP 需要 host")
	}
	dir := desktopTempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return RDPStart{}, err
	}
	file := filepath.Join(dir, fmt.Sprintf("tablepro-%d.rdp", time.Now().UnixNano()))
	if err := os.WriteFile(file, []byte(rdpFileContent(host, port, user)), 0o600); err != nil {
		return RDPStart{}, err
	}
	// Compensation FIRST: the file's deletion is the inverse of having written it.
	if err := r.store.Track(f, "rdp-file:"+file, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		return func(*ctxcore.Store) error {
			if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}, nil
	}); err != nil {
		_ = os.Remove(file)
		return RDPStart{}, err
	}
	// Now the emission: spawn the client (no inverse exists for a window).
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return RDPStart{}, fmt.Errorf("desktop plugin process is not running")
	}
	if err := p.call(context.Background(), workbench.MethodChannelOpen, map[string]any{
		"kind": "rdp", "host": host, "port": port, "user": user, "file": file,
	}, nil); err != nil {
		// The file's inverse still holds: the failed spawn leaves no debris.
		return RDPStart{}, err
	}
	// The session row records what was launched (the file is already tracked).
	sessionTag := ctxcore.Tag("rdp/" + filepath.Base(file))
	if err := r.store.Track(f, "rdp-session:"+string(sessionTag), func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableInsert(ctxcore.KeyDesktopSession, sessionTag, f.UID(),
			map[string]any{"kind": "rdp", "target": host, "file": file}); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyDesktopSession)
		return func(st2 *ctxcore.Store) error {
			if err := st2.TableRemove(ctxcore.KeyDesktopSession, sessionTag); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyDesktopSession)
			return nil
		}, nil
	}); err != nil {
		return RDPStart{}, err
	}
	return RDPStart{
		File: file, Session: string(sessionTag), Launched: true,
		Target: fmt.Sprintf("%s:%d", host, port),
		Notice: "RDP 客户端已拉起（发射，不可撤回）；.rdp 临时文件已登记清理逆，卸载或关闭会话即删除。",
	}, nil
}

// VNCStart is an open VNC relay.
type VNCStart struct {
	Session string `json:"session"`
	Target  string `json:"target"`
}

// OpenVNCSession dials the VNC server through the plugin and binds the session row.
func (r *Runner) OpenVNCSession(host string, port int) (VNCStart, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return VNCStart{}, fmt.Errorf("%s: the desktop plugin fiber is not active", workbench.CodeInactive)
	}
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return VNCStart{}, fmt.Errorf("desktop plugin process is not running")
	}
	var res struct {
		ChannelID string `json:"channelId"`
	}
	if err := p.call(context.Background(), workbench.MethodChannelOpen, map[string]any{
		"kind": "vnc", "host": host, "port": port,
	}, &res); err != nil {
		return VNCStart{}, err
	}
	tag := ctxcore.Tag(res.ChannelID)
	if err := r.store.Track(f, "vnc:"+res.ChannelID, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableInsert(ctxcore.KeyDesktopSession, tag, f.UID(),
			map[string]any{"kind": "vnc", "target": fmt.Sprintf("%s:%d", host, port)}); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyDesktopSession)
		return func(st2 *ctxcore.Store) error {
			// The inverse closes the socket AND withdraws the row: a relay is only
			// withdrawn when both halves are gone.
			_ = r.closeDesktopChannel(workbench.MethodChannelClose, map[string]any{"channelId": res.ChannelID})
			if err := st2.TableRemove(ctxcore.KeyDesktopSession, tag); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyDesktopSession)
			return nil
		}, nil
	}); err != nil {
		_ = r.closeDesktopChannel(workbench.MethodChannelClose, map[string]any{"channelId": res.ChannelID})
		return VNCStart{}, err
	}
	return VNCStart{Session: res.ChannelID, Target: fmt.Sprintf("%s:%d", host, port)}, nil
}

// VNCRelay sends bytes to the server and returns whatever it answered now.
// The bytes are base64 on the wire (the channel envelope is JSON) and raw to the
// caller; an empty answer with ended=false is the normal "nothing pending" case.
func (r *Runner) VNCRelay(session string, payload []byte) ([]byte, bool, error) {
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return nil, true, fmt.Errorf("desktop plugin process is not running")
	}
	var res struct {
		Data  string `json:"data"`
		Ended bool   `json:"ended"`
	}
	if err := p.call(context.Background(), workbench.MethodChannelData, map[string]any{
		"channelId": session,
		"data":      base64.StdEncoding.EncodeToString(payload),
	}, &res); err != nil {
		return nil, true, err
	}
	out, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		return nil, true, fmt.Errorf("bad base64 from plugin: %w", err)
	}
	return out, res.Ended, nil
}

// closeDesktopChannel is the plugin call shared by the inverse and the explicit close.
func (r *Runner) closeDesktopChannel(method string, params map[string]any) error {
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return nil
	}
	return p.call(context.Background(), method, params, nil)
}

// CloseDesktopSession drops one desktop session: for VNC it closes the relay and
// the row; for RDP it removes the .rdp file and the row (the client window, being
// an emission, stays until the operator closes it).
func (r *Runner) CloseDesktopSession(session string) error {
	tag := ctxcore.Tag(session)
	var value any
	if v, ok := r.store.Get(ctxcore.KeyDesktopSession, tag); ok {
		value = v
	}
	// VNC: the channel id IS the session tag.
	if strings.HasPrefix(string(tag), "vnc-") {
		_ = r.closeDesktopChannel(workbench.MethodChannelClose, map[string]any{"channelId": string(tag)})
	}
	// RDP: remove the file the row points at (idempotent; the inverse may race).
	if m, ok := value.(map[string]any); ok {
		if kind, _ := m["kind"].(string); kind == "rdp" {
			if file, _ := m["file"].(string); file != "" {
				if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	}
	if err := r.store.TableRemove(ctxcore.KeyDesktopSession, tag); err != nil {
		// An absent row is not a failure here: the accumulator may have withdrawn
		// it a moment earlier during an unload.
		return nil
	}
	r.store.Notify(ctxcore.KeyDesktopSession)
	return nil
}

// DesktopSessions lists the live desktop rows for the panel.
func (r *Runner) DesktopSessions() []map[string]any {
	sigma := r.store.Sigma()
	out := make([]map[string]any, 0, len(sigma[ctxcore.KeyDesktopSession]))
	for _, b := range sigma[ctxcore.KeyDesktopSession] {
		row := map[string]any{"tag": string(b.Tag)}
		if m, ok := b.Value.(map[string]any); ok {
			for k, v := range m {
				row[k] = v
			}
		}
		out = append(out, row)
	}
	return out
}
