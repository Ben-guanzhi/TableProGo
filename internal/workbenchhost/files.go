package workbenchhost

// files.go — W-F1's host half: the workbench host drives the plugin's
// files.session through the same withhold/track machinery as the terminal.
//
// THE ACQUISITION CHAIN (§6.1, operationalized):
//
//	upload: stage to remote .tmp (plugin) → Track the tmp-cleanup inverse →
//	        commit rename (plugin) → Track the rename-reverse inverse.
//	  The tmp cleanup is what makes a FAILED transfer recoverable (the plan's
//	  §4 obligation #3: "半完成的 SFTP 传输——.tmp 残留回收登记为效应逆").
//	  The commit's inverse restores the pre-commit state (the .tmp still on
//	  disk), so even a commit is a reversible acquisition.
//	download: the plugin streams into a LOCAL tmp; the host renames atomically
//	          (same volume) and Tracks the file's deletion as the inverse — a
//	  downloaded file is a local acquisition, removable on unload.
//	download of a directory listing: pure read, no tracking.
//
// REALM ENFORCEMENT (Σiso, Def 24/25 — defense in depth): the session was
// opened with allowed roots; EVERY path the UI sends is checked here BEFORE it
// reaches the plugin, in addition to the plugin's own check. The two-layer
// mapping (key → realm → value) is realized as "one files.session binding per
// realm id"; a session that escaped its realm would be a host-side bug.

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/dbcore/dbcore/plugindriver/workbench"
	"tablepro-go/internal/ctxcore"
)

// OpenFilesSession asks the plugin for an SFTP session with the realm roots and
// binds it under files.session (a runtime effect, tracked like a channel).
func (r *Runner) OpenFilesSession(host string, port int, user, password string, roots []string) (string, error) {
	r.mu.Lock()
	p, f := r.proc, r.fiber
	r.mu.Unlock()
	if p == nil || f == nil || p.Dead() {
		return "", fmt.Errorf("plugin process is not running")
	}
	var res struct {
		ChannelID string `json:"channelId"`
	}
	if err := p.call(context.Background(), workbench.MethodFilesAction, map[string]any{
		"action": "open", "host": host, "port": port, "user": user,
		"password": password, "roots": roots,
	}, &res); err != nil {
		return "", err
	}
	err := r.store.Track(f, "open-files:"+res.ChannelID, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableInsert(ctxcore.KeyFilesSession, ctxcore.Tag(res.ChannelID), f.UID(),
			map[string]any{"roots": roots}); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyFilesSession)
		return func(st2 *ctxcore.Store) error {
			if err := st2.TableRemove(ctxcore.KeyFilesSession, ctxcore.Tag(res.ChannelID)); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyFilesSession)
			return nil
		}, nil
	})
	if err != nil {
		return "", err
	}
	return res.ChannelID, nil
}

// filesCall issues one file action to the plugin and decodes the result into
// out. The realm check runs here too — the HOST is the second gate.
func (r *Runner) filesCall(session, action string, params map[string]any, out any, realmPaths ...string) error {
	r.mu.Lock()
	p := r.proc
	r.mu.Unlock()
	if p == nil || p.Dead() {
		return fmt.Errorf("plugin process is not running")
	}
	for _, rp := range realmPaths {
		if !r.pathInRealm(session, rp) {
			return fmt.Errorf("路径 %s 超出本会话的白名单（隔离 realm Σiso）", rp)
		}
	}
	full := map[string]any{"channelId": session, "action": action}
	for k, v := range params {
		full[k] = v
	}
	return p.call(context.Background(), workbench.MethodFilesAction, full, out)
}

// pathInRealm re-checks the session's roots host-side. The roots were recorded
// in the files.session binding when the session opened; a session whose binding
// is gone (fiber deactivated) is out of every realm.
//
// Normalization: both the path and each root are path.Clean-ed (resolving any
// traversal BEFORE the comparison — "/data/../etc" is "/etc"), leading "/"
// stripped, backslashes forward. No roots at all = dev mode (allow all). A
// nil-vs-empty roots distinction does not exist here: empty = no limits.
func (r *Runner) pathInRealm(session, p string) bool {
	v, ok := r.store.Get(ctxcore.KeyFilesSession, ctxcore.Tag(session))
	if !ok {
		return false
	}
	m, _ := v.(map[string]any)
	var roots []string
	switch raw := m["roots"].(type) {
	case []string:
		roots = raw
	case []any:
		for _, rv := range raw {
			if s, ok := rv.(string); ok {
				roots = append(roots, s)
			}
		}
	}
	clean := strings.TrimPrefix(pathClean(filepath.ToSlash(p)), "/")
	if len(roots) == 0 {
		return true // the session declared no limits
	}
	for _, root := range roots {
		rc := strings.Trim(strings.TrimPrefix(pathClean(filepath.ToSlash(root)), "/"), "/")
		if rc == "" {
			return true
		}
		if clean == rc || strings.HasPrefix(clean, rc+"/") {
			return true
		}
	}
	return false
}

// pathClean is SFTP-path Clean: forward slashes, POSIX rules (the servers are
// unix-y even when the host is Windows).
func pathClean(p string) string {
	return strings.TrimSuffix(filepath.ToSlash(path.Clean(p)), "/")
}

// ListFiles reads a directory through the session.
func (r *Runner) ListFiles(session, dir string) (any, error) {
	var out struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := r.filesCall(session, "list", map[string]any{"path": dir}, &out, dir); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// MkdirFiles creates a remote directory inside the realm.
func (r *Runner) MkdirFiles(session, dir string) error {
	return r.filesCall(session, "mkdir", map[string]any{"path": dir}, nil, dir)
}

// RenameFiles renames/moves within the realm (both ends checked).
func (r *Runner) RenameFiles(session, from, to string) error {
	return r.filesCall(session, "rename", map[string]any{"path": from, "to": to}, nil, from, to)
}

// RemoveFiles deletes a remote file inside the realm.
func (r *Runner) RemoveFiles(session, path string) error {
	return r.filesCall(session, "remove", map[string]any{"path": path}, nil, path)
}

// UploadFile stages a local file to remote .tmp (the plugin side), Tracks the
// tmp-cleanup inverse, commits the rename, and Tracks the commit's inverse. The
// return value is the committed remote path.
func (r *Runner) UploadFile(session string, localPath, remotePath string) (string, int64, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return "", 0, fmt.Errorf("plugin fiber is not active")
	}
	var staged struct {
		Tmp  string `json:"tmp"`
		Size int64  `json:"size"`
	}
	if err := r.filesCall(session, "upload",
		map[string]any{"localTmp": localPath, "path": remotePath}, &staged, remotePath); err != nil {
		return "", 0, err
	}
	// Inverse 1: the .tmp residue (a failed COMMIT leaves it on the remote).
	if err := r.store.Track(f, "stage-upload:"+staged.Tmp, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		tmp := staged.Tmp
		session := session
		return func(st2 *ctxcore.Store) error {
			// The residue only exists if the commit did not rename it away; the
			// discard action is idempotent on a missing path.
			_ = r.filesCall(session, "discard-upload", map[string]any{"tmp": tmp}, nil)
			return nil
		}, nil
	}); err != nil {
		return "", 0, err
	}
	// Commit.
	if err := r.filesCall(session, "commit-upload",
		map[string]any{"tmp": staged.Tmp, "path": remotePath}, nil, staged.Tmp, remotePath); err != nil {
		return "", staged.Size, err
	}
	// Inverse 2: the commit's reverse (restore the .tmp state). The inverse
	// renames BACK and re-stages nothing — the tmp was consumed by the rename,
	// so the reverse re-creates the pre-commit name from the committed file.
	// This is the acquisition discipline: even a commit is reversible.
	if err := r.store.Track(f, "commit-upload:"+remotePath, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		remotePath := remotePath
		tmp := staged.Tmp
		session := session
		return func(st2 *ctxcore.Store) error {
			_ = r.filesCall(session, "rename", map[string]any{"path": remotePath, "to": tmp}, nil, remotePath, tmp)
			return nil
		}, nil
	}); err != nil {
		return "", staged.Size, err
	}
	return remotePath, staged.Size, nil
}

// DownloadFile streams a remote file into a local .tmp and renames it into
// place atomically. The inverse (the local file's deletion) is tracked on the
// fiber: a downloaded file is a LOCAL acquisition.
func (r *Runner) DownloadFile(session, remotePath, localTarget string) (int64, error) {
	f := r.Fiber()
	if f == nil || f.State() != ctxcore.Active {
		return 0, fmt.Errorf("plugin fiber is not active")
	}
	localTmp := localTarget + ".tmp"
	var res struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := r.filesCall(session, "download",
		map[string]any{"path": remotePath, "localTmp": localTmp}, &res, remotePath); err != nil {
		return 0, err
	}
	// Atomic commit on the local volume.
	if err := os.Rename(localTmp, localTarget); err != nil {
		_ = os.Remove(localTmp)
		return 0, err
	}
	if err := r.store.Track(f, "download:"+localTarget, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		target := localTarget
		return func(st2 *ctxcore.Store) error {
			_ = os.Remove(target)
			return nil
		}, nil
	}); err != nil {
		return res.Size, err
	}
	return res.Size, nil
}

// CloseFilesSession drops the session and its binding (Track's mirror image).
func (r *Runner) CloseFilesSession(session string) error {
	f := r.Fiber()
	if f == nil {
		return fmt.Errorf("plugin fiber is not active")
	}
	if err := r.filesCall(session, "close", map[string]any{}, nil); err != nil {
		// The plugin may already be dead; the binding still has to go.
		_ = err
	}
	return r.store.Track(f, "close-files:"+session, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableRemove(ctxcore.KeyFilesSession, ctxcore.Tag(session)); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyFilesSession)
		return func(st2 *ctxcore.Store) error {
			if err := st2.TableInsert(ctxcore.KeyFilesSession, ctxcore.Tag(session), f.UID(),
				map[string]any{"reopened": true}); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyFilesSession)
			return nil
		}, nil
	})
}
