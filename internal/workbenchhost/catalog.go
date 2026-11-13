package workbenchhost

// catalog.go — W-S1's extension catalog: fetch a JSON index, verify digests, and
// classify each entry as installable / installed / updatable.
//
// THE FORMAT is deliberately boring and portable: a JSON document with an
// `entries` array. Git (raw file) and WebDAV both serve JSON over HTTP, so one
// fetcher covers both — "Git/WebDAV" is a property of WHERE the file lives, not of
// a protocol this package implements.
//
// THE TRUST BOUNDARY is the digest. An entry carries `sha256:<hex>` of the exact
// binary; install downloads it, hashes it, and REFUSES on a mismatch. That is the
// plan's "market signature digest check" in its simplest honest form: a digest is
// integrity, not authenticity — signing needs a key distribution story, which is
// the documented follow-up (the same reason pluginreg's trust gate is a separate
// file-identity boundary).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dbcore/dbcore/plugindriver/workbench"
)

// CatalogEntry is one installable plugin.
type CatalogEntry struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Version     string                 `json:"version"`
	Digest      string                 `json:"digest"` // sha256:<hex>
	URL         string                 `json:"url"`
	Description string                 `json:"description,omitempty"`
	Provides    []workbench.Capability `json:"provides,omitempty"`
	Depends     []workbench.Capability `json:"depends,omitempty"`
}

// Catalog is the fetched index.
type Catalog struct {
	Updated string         `json:"updated,omitempty"`
	Entries []CatalogEntry `json:"entries"`
}

// InstalledPlugin is the local manifest of an installed entry. Only these fields
// are synced to a team (manifest only: no binary, no secret).
type InstalledPlugin struct {
	ID       string                 `json:"id"`
	Version  string                 `json:"version"`
	Digest   string                 `json:"digest"`
	Path     string                 `json:"path,omitempty"`
	Provides []workbench.Capability `json:"provides,omitempty"`
	Depends  []workbench.Capability `json:"depends,omitempty"`
}

// ExtensionRow is what the extensions page renders.
type ExtensionRow struct {
	CatalogEntry
	State string `json:"state"` // installable | installed | updatable
	Local string `json:"localVersion,omitempty"`
}

// MaxCatalogBytes bounds the index fetch.
const MaxCatalogBytes = 4 << 20

// Validate checks the fields an install depends on.
func (e CatalogEntry) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("catalog entry has no id")
	}
	if e.Version == "" {
		return fmt.Errorf("entry %s has no version", e.ID)
	}
	if e.URL == "" {
		return fmt.Errorf("entry %s has no url", e.ID)
	}
	if !strings.HasPrefix(e.Digest, "sha256:") || len(e.Digest) != len("sha256:")+64 {
		return fmt.Errorf("entry %s has a bad digest %q (want sha256:<64 hex>)", e.ID, e.Digest)
	}
	return nil
}

// FetchCatalog reads the index over HTTP(S).
func FetchCatalog(ctx context.Context, url string) (*Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("目录不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("目录返回 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxCatalogBytes))
	if err != nil {
		return nil, err
	}
	var cat Catalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("目录不是预期的 JSON: %w", err)
	}
	for _, e := range cat.Entries {
		if err := e.Validate(); err != nil {
			return nil, err
		}
	}
	return &cat, nil
}

// Classify compares the catalog against what is installed. A same-version entry is
// "installed"; a different version is "updatable" (the catalog is the newer
// statement by construction — it is a published index, not a peer).
func Classify(cat *Catalog, installed []InstalledPlugin) []ExtensionRow {
	have := map[string]InstalledPlugin{}
	for _, p := range installed {
		have[p.ID] = p
	}
	rows := make([]ExtensionRow, 0, len(cat.Entries))
	for _, e := range cat.Entries {
		row := ExtensionRow{CatalogEntry: e, State: "installable"}
		if local, ok := have[e.ID]; ok {
			if local.Version == e.Version && local.Digest == e.Digest {
				row.State = "installed"
			} else {
				row.State = "updatable"
				row.Local = local.Version
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

// pluginsDirOverride is the test seam for the install root.
var pluginsDirOverride string

// PluginsDir is where installed plugins live.
func PluginsDir() string {
	if pluginsDirOverride != "" {
		return pluginsDirOverride
	}
	return userDataDirOrDefault("plugins")
}

// LoadInstalled reads every <dir>/<id>/plugin.json manifest.
func LoadInstalled(dir string) ([]InstalledPlugin, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []InstalledPlugin
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name(), "plugin.json"))
		if err != nil {
			continue // a directory without a manifest is not an installed plugin
		}
		var p InstalledPlugin
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		p.Path = filepath.Join(dir, e.Name())
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// InstallEntry downloads the entry's binary, verifies its digest, and writes the
// manifest. A mismatch REFUSES and leaves nothing behind: a half-installed plugin
// whose bytes do not match the index is worse than an absent one.
func InstallEntry(ctx context.Context, entry CatalogEntry, dir string) (InstalledPlugin, error) {
	if err := entry.Validate(); err != nil {
		return InstalledPlugin{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
	if err != nil {
		return InstalledPlugin{}, err
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return InstalledPlugin{}, fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return InstalledPlugin{}, fmt.Errorf("下载返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return InstalledPlugin{}, err
	}
	sum := sha256.Sum256(body)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, entry.Digest) {
		return InstalledPlugin{}, fmt.Errorf("摘要不匹配（期望 %s，实得 %s）：拒绝安装", entry.Digest, got)
	}
	target := filepath.Join(dir, entry.ID)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return InstalledPlugin{}, err
	}
	binName := entry.ID
	if strings.HasSuffix(strings.ToLower(entry.URL), ".exe") || filepath.Separator == '\\' {
		binName += ".exe"
	}
	binPath := filepath.Join(target, binName)
	if err := os.WriteFile(binPath, body, 0o700); err != nil {
		return InstalledPlugin{}, err
	}
	local := InstalledPlugin{
		ID: entry.ID, Version: entry.Version, Digest: got, Path: target,
		Provides: entry.Provides, Depends: entry.Depends,
	}
	raw, _ := json.MarshalIndent(local, "", "  ")
	if err := os.WriteFile(filepath.Join(target, "plugin.json"), raw, 0o600); err != nil {
		return InstalledPlugin{}, err
	}
	return local, nil
}
