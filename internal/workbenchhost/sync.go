package workbenchhost

// sync.go — W-S1's team sync.
//
// WHAT TRAVELS AND WHAT DOES NOT, and this is the whole discipline:
//
//	plugins      the MANIFEST only (id, version, digest, provides, depends) — no
//	             binaries. A teammate installs from the catalog by digest.
//	connections  the ARCHIVE only: id/name/engine/host/database. NEVER a password,
//	             passphrase, token or key — the "秘密为 ref" rule, realized as a
//	             SCRUBBER that walks the values and drops anything secret-shaped,
//	             because a rule that depends on every call site remembering is the
//	             R2 shape the study criticizes.
//
// LWW + conflict copies: the document carries UpdatedAt. A pull with a newer remote
// wins; a push onto a newer remote REFUSES and writes the remote to a
// `.conflict-<ts>.json` copy first, so the loser is never silently discarded. That
// is a two-way sync with a visible loser, not a merge heuristic pretending to be
// safe.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SyncDoc is the synced document.
type SyncDoc struct {
	Version     int               `json:"version"`
	UpdatedAt   string            `json:"updatedAt"`
	Connections []map[string]any  `json:"connections"`
	Plugins     []InstalledPlugin `json:"plugins"`
}

// secretKeyShapes are the field names that must never leave the machine.
var secretKeyShapes = []string{
	"password", "passwd", "passphrase", "secret", "token", "apikey", "api_key",
	"privatekey", "private_key", "credential", "keychain", "ref",
}

// ScrubSecrets removes secret-shaped keys from one map, recursively. It is applied
// to every connection before the document is built, so a caller cannot leak a
// password by forgetting to strip it.
func ScrubSecrets(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		lower := strings.ToLower(k)
		secret := false
		for _, shape := range secretKeyShapes {
			if strings.Contains(lower, shape) {
				secret = true
				break
			}
		}
		if secret {
			continue
		}
		switch tv := v.(type) {
		case map[string]any:
			out[k] = ScrubSecrets(tv)
		default:
			out[k] = v
		}
	}
	return out
}

// BuildSyncDoc assembles the document: connections scrubbed, plugins as manifests.
func BuildSyncDoc(conns []map[string]any, plugins []InstalledPlugin) SyncDoc {
	scrubbed := make([]map[string]any, 0, len(conns))
	for _, c := range conns {
		scrubbed = append(scrubbed, ScrubSecrets(c))
	}
	sort.Slice(scrubbed, func(i, j int) bool {
		a, _ := scrubbed[i]["id"].(string)
		b, _ := scrubbed[j]["id"].(string)
		return a < b
	})
	manifests := make([]InstalledPlugin, 0, len(plugins))
	for _, p := range plugins {
		manifests = append(manifests, InstalledPlugin{
			ID: p.ID, Version: p.Version, Digest: p.Digest,
			Provides: p.Provides, Depends: p.Depends,
		})
	}
	return SyncDoc{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339), Connections: scrubbed, Plugins: manifests}
}

// SyncPull fetches the remote document.
func SyncPull(ctx context.Context, url string) (SyncDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return SyncDoc{}, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return SyncDoc{}, fmt.Errorf("同步源不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return SyncDoc{}, nil // an empty remote is a first push, not an error
	}
	if resp.StatusCode != 200 {
		return SyncDoc{}, fmt.Errorf("同步源返回 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return SyncDoc{}, err
	}
	var doc SyncDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return SyncDoc{}, fmt.Errorf("远程文档不是预期的 JSON: %w", err)
	}
	// Scrub AGAIN: a hostile or stale remote must not be able to plant a secret in
	// the local document through a pull.
	for i := range doc.Connections {
		doc.Connections[i] = ScrubSecrets(doc.Connections[i])
	}
	return doc, nil
}

// SyncPush writes the document (WebDAV PUT / Git host's file API both speak this).
func SyncPush(ctx context.Context, url string, doc SyncDoc) error {
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("同步推送失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("同步推送返回 %d", resp.StatusCode)
	}
	return nil
}

// MergeSync decides which side wins by UpdatedAt (LWW). conflict reports that the
// LOCAL document lost — the caller writes the remote to a conflict copy before
// overwriting, so nothing is lost silently.
func MergeSync(local, remote SyncDoc) (winner SyncDoc, conflict bool) {
	if remote.UpdatedAt == "" {
		return local, false // nothing remote: local wins by default
	}
	if local.UpdatedAt == "" {
		return remote, true
	}
	lt, lerr := time.Parse(time.RFC3339, local.UpdatedAt)
	rt, rerr := time.Parse(time.RFC3339, remote.UpdatedAt)
	if lerr != nil || rerr != nil {
		// Unparseable timestamps fall back to "remote wins" — the conservative
		// choice: a document we cannot date is not evidence of being newer.
		return remote, true
	}
	if rt.After(lt) {
		return remote, true
	}
	return local, false
}

// WriteConflictCopy writes the losing remote document next to the local one.
func WriteConflictCopy(dir string, doc SyncDoc) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("sync-conflict-%s.json", time.Now().UTC().Format("20060102-150405")))
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
