package pluginreg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile drops bytes into a temp dir and returns the path.
func writeFile(t *testing.T, dir, name string, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---- hashing ----

func TestHashFileIsSHA256OfTheBytes(t *testing.T) {
	dir := t.TempDir()
	// The digest of "abc" is the canonical SHA-256 test vector.
	path := writeFile(t, dir, "p", "abc")
	got, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
}

func TestHashFileMissingIsAnError(t *testing.T) {
	if _, err := HashFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("a missing file has no digest and must error")
	}
}

// ---- the three trust states ----

func TestVerifyUnknownWhenNeverTrusted(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "plugin", "binary")
	check, err := Verify(path, "", map[string]string{}, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if check.State != StateUnknown {
		t.Fatalf("state = %s, want unknown", check.State)
	}
	if !check.OK() {
		t.Fatal("unknown must be runnable: forcing trust on every dev plugin would break the workflow")
	}
}

func TestVerifyTrustedWhenDigestMatches(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "plugin", "binary")
	digest, _ := HashFile(path)
	check, err := Verify(path, "", map[string]string{mustAbs(t, path): digest}, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if check.State != StateTrusted {
		t.Fatalf("state = %s, want trusted", check.State)
	}
	if !check.OK() {
		t.Fatal("trusted must be runnable")
	}
}

func TestVerifyTamperedWhenBytesChanged(t *testing.T) {
	// The whole point of the store: a file that WAS trusted and then changed is
	// the substitution attack. It must be refused, not warned about.
	dir := t.TempDir()
	path := writeFile(t, dir, "plugin", "original")
	digest, _ := HashFile(path)
	// Replace the bytes after trusting.
	if err := os.WriteFile(path, []byte("swapped"), 0o755); err != nil {
		t.Fatal(err)
	}
	check, err := Verify(path, "", map[string]string{mustAbs(t, path): digest}, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if check.State != StateTampered {
		t.Fatalf("state = %s, want tampered", check.State)
	}
	if check.OK() {
		t.Fatal("a tampered plugin must be refused")
	}
	if check.Expected == check.Actual {
		t.Fatal("a tamper report must carry both digests")
	}
	// The message has to name both sides, or the user cannot tell what changed.
	detail := check.Detail()
	if !strings.Contains(detail, "摘要不符") || !strings.Contains(detail, "期望") {
		t.Fatalf("detail = %q", detail)
	}
}

func TestVerifyRegistryDigestWithoutStoreEntry(t *testing.T) {
	// A registry entry that publishes a digest verifies against it even when the
	// user has not trusted the path yet: the registry author vouched for these
	// bytes. A mismatch is still a tamper.
	dir := t.TempDir()
	path := writeFile(t, dir, "plugin", "binary")
	digest, _ := HashFile(path)
	ok, err := Verify(path, digest, map[string]string{}, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if ok.State != StateTrusted {
		t.Fatalf("state = %s, want trusted from the registry digest", ok.State)
	}
	bad, err := Verify(path, strings.Repeat("0", 64), map[string]string{}, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if bad.State != StateTampered {
		t.Fatalf("state = %s, want tampered", bad.State)
	}
}

func TestVerifyStoreEntryOverridesRegistryDigest(t *testing.T) {
	// The user's own trust entry is authoritative: they re-trusted a rebuilt
	// plugin, and the (stale) registry digest must not override that.
	dir := t.TempDir()
	path := writeFile(t, dir, "plugin", "rebuilt")
	digest, _ := HashFile(path)
	check, err := Verify(path, strings.Repeat("0", 64), map[string]string{mustAbs(t, path): digest}, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if check.State != StateTrusted {
		t.Fatalf("state = %s, want trusted from the store", check.State)
	}
}

func TestVerifyMissingFileIsAnError(t *testing.T) {
	if _, err := Verify(filepath.Join(t.TempDir(), "gone"), "", nil, "x"); err == nil {
		t.Fatal("verifying a missing file must error")
	}
}

// ---- trust store maintenance ----

func TestTrustRecordsTheCurrentDigest(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "plugin", "binary")
	store := &TrustStore{}
	entry, err := Trust(store, path, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path != mustAbs(t, path) || entry.SHA256 == "" {
		t.Fatalf("entry = %+v", entry)
	}
	if len(store.Plugins) != 1 {
		t.Fatalf("store = %+v", store.Plugins)
	}
	// Trusting again replaces rather than duplicating.
	if _, err := Trust(store, path, "duckdb"); err != nil {
		t.Fatal(err)
	}
	if len(store.Plugins) != 1 {
		t.Fatalf("a re-trust must replace, not duplicate: %+v", store.Plugins)
	}
}

func TestUntrustRemovesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "plugin", "binary")
	store := &TrustStore{}
	if _, err := Trust(store, path, "duckdb"); err != nil {
		t.Fatal(err)
	}
	if !Untrust(store, path) {
		t.Fatal("removing a trusted path should report a removal")
	}
	if len(store.Plugins) != 0 {
		t.Fatalf("store = %+v", store.Plugins)
	}
	// Removing again is not an error: the caller wanted it gone and it is.
	if Untrust(store, path) {
		t.Fatal("removing an absent path should report no removal")
	}
}

// ---- file formats ----

func TestRegistryResolvesPathsRelativeToItself(t *testing.T) {
	// A registry travelling with a plugins/ folder must mean "next to me", not
	// "relative to the process's working directory".
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	body := `{"schema":1,"plugins":[{"type":"duckdb","name":"DuckDB","path":"plugins/duckdb.exe"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "plugins", "duckdb.exe")
	if reg.Plugins[0].Path != want {
		t.Fatalf("path = %q, want %q", reg.Plugins[0].Path, want)
	}
}

func TestRegistryMissingFileIsEmptyNotAnError(t *testing.T) {
	reg, err := LoadRegistry(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatalf("a missing registry is normal: %v", err)
	}
	if len(reg.Plugins) != 0 {
		t.Fatalf("reg = %+v", reg)
	}
}

func TestRegistryRefusesUnknownSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	if err := os.WriteFile(path, []byte(`{"schema":99,"plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); err == nil {
		t.Fatal("an unknown schema must be refused, not guessed at")
	}
}

func TestRegistryToleratesBOM(t *testing.T) {
	// A manifest written by Notepad/PowerShell carries a BOM; refusing it is a
	// failure over three invisible bytes.
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"schema":1,"plugins":[]}`)...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); err != nil {
		t.Fatalf("a BOM must be tolerated: %v", err)
	}
}

func TestTrustStoreRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "plugin", "binary")
	store := &TrustStore{}
	if _, err := Trust(store, path, "duckdb"); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "trust.json")
	if err := SaveTrustStore(storePath, store); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadTrustStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Plugins) != 1 || loaded.Plugins[0].SHA256 != store.Plugins[0].SHA256 {
		t.Fatalf("loaded = %+v", loaded.Plugins)
	}
	if loaded.Schema != TrustSchema {
		t.Fatalf("schema = %d", loaded.Schema)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	// The rename is the commit point: a reader must never see a truncated file.
	// This asserts the visible behaviour — after a save the file parses — and
	// that no temp file is left behind.
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")
	store := &TrustStore{}
	if err := SaveTrustStore(path, store); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustStore(path); err != nil {
		t.Fatalf("saved file does not parse: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pluginreg-") {
			t.Fatalf("a temp file was left behind: %s", e.Name())
		}
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
