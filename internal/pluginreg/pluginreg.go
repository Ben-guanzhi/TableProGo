// Package pluginreg decides which plugin executables this process is willing to
// run.
//
// The workspace's plugin model is a child process speaking the dbcore protocol
// (dbcore/plugindriver): adding an engine means pointing at an executable. That
// is convenient and it is also the one place where the app executes a file the
// user did not build — so the file's identity has to be checked BEFORE the
// launch, not after. The design mirrors the archived dbstudio trust gate: hash
// the bytes first, then (and only then) hand the path to the launcher.
//
// Three states, and they are not symmetric:
//
//	Trusted  the path is in the trust store and the digest matches  -> run
//	Unknown  the path was never trusted (a fresh dev plugin)        -> run, but say so
//	Tampered the path is trusted but the bytes changed             -> refuse
//
// That asymmetry is the whole design: a file nobody vouched for is a plugin you
// chose to try, while a vetted file whose bytes moved is exactly the substitution
// attack the store exists to catch. Refusing Unknown would break the existing
// `TABLEPRO_PLUGIN_<TYPE>=path` workflow; allowing Tampered would make the store
// decorative.
package pluginreg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// State is the outcome of checking one plugin executable.
type State string

const (
	// StateTrusted means the digest matches the trust store.
	StateTrusted State = "trusted"
	// StateUnknown means no trust entry exists; the caller may run the plugin
	// but should surface that it was not vouched for.
	StateUnknown State = "unknown"
	// StateTampered means a trust entry exists and the bytes changed.
	StateTampered State = "tampered"
)

// Entry is one plugin a registry offers.
type Entry struct {
	// Type is the engine type the plugin provides ("duckdb", "clickhouse", …).
	Type string `json:"type"`
	// Name is the human name shown in the plugins panel.
	Name string `json:"name"`
	// Version is the plugin's own version string (informational).
	Version string `json:"version,omitempty"`
	// Path is relative to the registry file's directory, or absolute.
	Path string `json:"path"`
	// SHA256 is the expected digest of the executable. Empty means the registry
	// author did not publish one: the plugin then lands in the "unknown" bucket
	// rather than being silently trusted.
	SHA256 string `json:"sha256,omitempty"`
	// Description is one line for the panel.
	Description string `json:"description,omitempty"`
	// Enabled decides whether startup registers it. Off by default: installing
	// and enabling are separate decisions, and a registry shipped by someone
	// else should not be able to add engines on its own.
	Enabled bool `json:"enabled,omitempty"`
}

// Registry is the set of plugins this installation knows about.
type Registry struct {
	// Schema is a version tag so a future format can be told apart. Unknown
	// values are refused rather than guessed at.
	Schema  int     `json:"schema"`
	Plugins []Entry `json:"plugins"`
}

// RegistrySchema is the only schema this build understands.
const RegistrySchema = 1

// TrustEntry records one executable the user has vouched for.
type TrustEntry struct {
	// Path is absolute: relative paths mean different files from different
	// working directories, which is not an identity.
	Path string `json:"path"`
	// SHA256 is the digest at the moment of trusting.
	SHA256 string `json:"sha256"`
	// Type is the engine, kept for display.
	Type string `json:"type,omitempty"`
}

// TrustStore is the vouched-for set.
type TrustStore struct {
	Schema  int          `json:"schema"`
	Plugins []TrustEntry `json:"plugins"`
}

// TrustSchema is the only schema this build understands.
const TrustSchema = 1

// LoadRegistry reads a registry file. A missing file yields an empty registry:
// most users never write one, and that is not an error.
func LoadRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Registry{Schema: RegistrySchema}, nil
		}
		return nil, fmt.Errorf("读取插件注册表: %w", err)
	}
	data = TrimBOM(data)
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("解析插件注册表 %s: %w", path, err)
	}
	if reg.Schema != 0 && reg.Schema != RegistrySchema {
		return nil, fmt.Errorf("插件注册表 %s 的 schema=%d，本版本只认 %d", path, reg.Schema, RegistrySchema)
	}
	// Resolve relative paths against the registry's own directory: a registry
	// that travels with a plugins/ folder must mean "next to me", not "relative
	// to wherever the app happened to start".
	base := filepath.Dir(path)
	for i := range reg.Plugins {
		if p := strings.TrimSpace(reg.Plugins[i].Path); p != "" && !filepath.IsAbs(p) {
			reg.Plugins[i].Path = filepath.Join(base, p)
		}
	}
	return &reg, nil
}

// SaveRegistry writes the registry atomically (temp file + rename): a crashed
// write must not leave a half-parsed file the next start chokes on.
func SaveRegistry(path string, reg *Registry) error {
	if reg.Schema == 0 {
		reg.Schema = RegistrySchema
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// LoadTrustStore reads the trust store. Missing means "nothing trusted yet",
// which is a normal state, not an error.
func LoadTrustStore(path string) (*TrustStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &TrustStore{Schema: TrustSchema}, nil
		}
		return nil, fmt.Errorf("读取插件信任清单: %w", err)
	}
	data = TrimBOM(data)
	var store TrustStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("解析插件信任清单 %s: %w", path, err)
	}
	if store.Schema != 0 && store.Schema != TrustSchema {
		return nil, fmt.Errorf("插件信任清单 %s 的 schema=%d，本版本只认 %d", path, store.Schema, TrustSchema)
	}
	return &store, nil
}

// SaveTrustStore writes the store atomically.
func SaveTrustStore(path string, store *TrustStore) error {
	if store.Schema == 0 {
		store.Schema = TrustSchema
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// Check is one executable's verification result.
type Check struct {
	// Path is absolute, as it will be launched.
	Path string
	// State is the trust verdict.
	State State
	// Expected is the digest the trust store (or registry) names.
	Expected string
	// Actual is the digest of the bytes on disk.
	Actual string
	// Type is the engine the entry claims, when known.
	Type string
}

// OK reports whether the plugin may run.
func (c Check) OK() bool { return c.State != StateTampered }

// Detail is a one-line human explanation for the panel and the log.
func (c Check) Detail() string {
	switch c.State {
	case StateTrusted:
		return "已在信任清单中，摘要一致"
	case StateUnknown:
		return "未在信任清单中（首次使用的插件，运行前请确认来源）"
	default:
		return fmt.Sprintf("摘要不符：期望 %s，实际 %s（文件在信任后已被替换）", short(c.Expected), short(c.Actual))
	}
}

// Verify decides whether one executable may run.
//
// The hash is computed from the file's BYTES, never from a handle the OS opened
// for execution: the check has to finish before anything runs the file, or it is
// theatre. A missing file is reported as an error (not a state) because there is
// no digest to compare and the launch would fail anyway.
func Verify(path string, want string, trusted map[string]string, typ string) (Check, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Check{}, fmt.Errorf("解析插件路径 %q: %w", path, err)
	}
	actual, err := HashFile(abs)
	if err != nil {
		return Check{}, fmt.Errorf("计算插件摘要 %s: %w", abs, err)
	}
	// The trust store is keyed by absolute path: two spellings of one file must
	// agree, and two different files must not collide.
	if known, ok := trusted[abs]; ok {
		want = known
	}
	check := Check{Path: abs, Actual: actual, Expected: want, Type: typ}
	switch {
	case want != "" && strings.EqualFold(want, actual):
		check.State = StateTrusted
	case want != "":
		check.State = StateTampered
	default:
		check.State = StateUnknown
	}
	return check, nil
}

// Trust records one executable's current digest, replacing any prior entry for
// the same path. It returns the entry it wrote so the caller can display what
// was vouched for.
func Trust(store *TrustStore, path, typ string) (TrustEntry, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return TrustEntry{}, err
	}
	digest, err := HashFile(abs)
	if err != nil {
		return TrustEntry{}, err
	}
	entry := TrustEntry{Path: abs, SHA256: digest, Type: typ}
	replaced := false
	for i := range store.Plugins {
		if store.Plugins[i].Path == abs {
			store.Plugins[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		store.Plugins = append(store.Plugins, entry)
	}
	sort.Slice(store.Plugins, func(i, j int) bool { return store.Plugins[i].Path < store.Plugins[j].Path })
	return entry, nil
}

// Untrust removes one path from the store. Removing a path that was not there is
// not an error: the caller wanted it gone, and it is.
func Untrust(store *TrustStore, path string) bool {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	kept := store.Plugins[:0]
	removed := false
	for _, e := range store.Plugins {
		if e.Path == path {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	store.Plugins = kept
	return removed
}

// HashFile computes the SHA-256 of a file's bytes.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// trustedMap indexes a store by absolute path.
func trustedMap(store *TrustStore) map[string]string {
	out := make(map[string]string, len(store.Plugins))
	for _, e := range store.Plugins {
		if e.Path == "" || e.SHA256 == "" {
			continue
		}
		out[e.Path] = e.SHA256
	}
	return out
}

// TrustedMap exposes the index to callers that hold a store.
func TrustedMap(store *TrustStore) map[string]string { return trustedMap(store) }

// TrimBOM strips a UTF-8 BOM. Windows editors write one and encoding/json
// refuses it; the same three invisible bytes turn a valid manifest into "corrupt".
func TrimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

// writeAtomic writes through a temp file in the same directory, then renames.
// The rename is the commit point: a reader either sees the old file or the new
// one, never a truncated one.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pluginreg-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName) // a failed rename leaves the temp behind otherwise
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// short abbreviates a digest for a one-line message.
func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "…"
}
