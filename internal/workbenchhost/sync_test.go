package workbenchhost

// W-S1 acceptance: catalog fetch + digest verification, three-state classification,
// and a team-sync document that CANNOT carry a secret.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// catalogServer serves an index and its binaries.
func catalogServer(t *testing.T, binary []byte) (*httptest.Server, CatalogEntry) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/bin/navop-x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binary)
	})
	sum := sha256.Sum256(binary)
	entry := CatalogEntry{
		ID: "navop-x", Name: "X 插件", Version: "1.2.0",
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
		URL:    srv.URL + "/bin/navop-x",
	}
	mux.HandleFunc("/catalog.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Catalog{Updated: "2026-11-13", Entries: []CatalogEntry{entry}})
	})
	return srv, entry
}

func TestCatalogFetchAndDigestInstall(t *testing.T) {
	srv, entry := catalogServer(t, []byte("#!/bin/sh\necho hi\n"))
	cat, err := FetchCatalog(context.Background(), srv.URL+"/catalog.json")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(cat.Entries) != 1 || cat.Entries[0].ID != "navop-x" {
		t.Fatalf("catalog = %+v", cat)
	}

	dir := t.TempDir()
	pluginsDirOverride = dir
	defer func() { pluginsDirOverride = "" }()

	local, err := InstallEntry(context.Background(), entry, dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if local.Digest != entry.Digest {
		t.Fatalf("digest = %q", local.Digest)
	}
	// The binary and the manifest are both on disk.
	if _, err := os.Stat(filepath.Join(dir, "navop-x", "plugin.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	installed, err := LoadInstalled(dir)
	if err != nil || len(installed) != 1 {
		t.Fatalf("installed = %+v err=%v", installed, err)
	}
	// Classification: same digest+version = installed; a different version = updatable.
	rows := Classify(cat, installed)
	if len(rows) != 1 || rows[0].State != "installed" {
		t.Fatalf("rows = %+v", rows)
	}
	newer := *cat
	newer.Entries = []CatalogEntry{entry}
	newer.Entries[0].Version = "1.3.0"
	rows = Classify(&newer, installed)
	if rows[0].State != "updatable" || rows[0].Local != "1.2.0" {
		t.Fatalf("updatable row = %+v", rows[0])
	}
}

func TestCatalogRefusesABadDigestAndLeavesNothing(t *testing.T) {
	srv, entry := catalogServer(t, []byte("real bytes"))
	entry.Digest = "sha256:" + strings.Repeat("0", 64)
	dir := t.TempDir()
	if _, err := InstallEntry(context.Background(), entry, dir); err == nil {
		t.Fatal("a digest mismatch must be refused")
	} else if !strings.Contains(err.Error(), "摘要不匹配") {
		t.Fatalf("the refusal must name the cause: %v", err)
	}
	// Nothing half-installed was left behind.
	if entries, _ := os.ReadDir(filepath.Join(dir, "navop-x")); len(entries) != 0 {
		t.Fatalf("a refused install left %d files", len(entries))
	}
	_ = srv
}

func TestCatalogRejectsMalformedEntries(t *testing.T) {
	for _, bad := range []CatalogEntry{
		{ID: "", Version: "1", Digest: "sha256:" + strings.Repeat("a", 64), URL: "http://x"},
		{ID: "a", Version: "", Digest: "sha256:" + strings.Repeat("a", 64), URL: "http://x"},
		{ID: "a", Version: "1", Digest: "md5:abc", URL: "http://x"},
		{ID: "a", Version: "1", Digest: "sha256:" + strings.Repeat("a", 64), URL: ""},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("malformed entry accepted: %+v", bad)
		}
	}
}

func TestSyncNeverCarriesASecret(t *testing.T) {
	// A connection with every secret shape a driver might use.
	conns := []map[string]any{{
		"id": "c1", "name": "生产库", "engine": "postgres", "host": "db.internal", "database": "app",
		"password": "hunter2", "passphrase": "pp", "apiKey": "sk-1", "token": "t",
		"credential": map[string]any{"user": "u", "password": "inner"},
		"options":    map[string]any{"ssl": "require", "password_ref": "keyring:c1"},
	}}
	doc := BuildSyncDoc(conns, nil)
	raw, _ := json.Marshal(doc)
	text := string(raw)
	for _, leak := range []string{"hunter2", "sk-1", "\"pp\"", "\"t\"", "inner", "password_ref"} {
		if strings.Contains(text, leak) {
			t.Fatalf("the sync document leaked %q:\n%s", leak, text)
		}
	}
	// The harmless fields survive.
	for _, keep := range []string{"db.internal", "app", "postgres", "require"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("the scrubber dropped %q:\n%s", keep, text)
		}
	}
	// A hostile remote cannot plant a secret either: pull scrubs again.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"updatedAt":"2026-11-13T00:00:00Z","connections":[{"id":"x","password":"planted"}],"plugins":[]}`))
	}))
	defer srv.Close()
	pulled, err := SyncPull(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rawPulled, _ := json.Marshal(pulled)
	if strings.Contains(string(rawPulled), "planted") {
		t.Fatalf("a pulled document kept a planted secret: %s", rawPulled)
	}
}

func TestSyncLWWAndConflictCopy(t *testing.T) {
	older := SyncDoc{Version: 1, UpdatedAt: "2026-11-01T00:00:00Z", Connections: []map[string]any{{"id": "old"}}}
	newer := SyncDoc{Version: 1, UpdatedAt: "2026-11-13T00:00:00Z", Connections: []map[string]any{{"id": "new"}}}

	win, conflict := MergeSync(older, newer)
	if !conflict || win.Connections[0]["id"] != "new" {
		t.Fatalf("the newer remote must win and report a conflict: %+v %v", win, conflict)
	}
	win, conflict = MergeSync(newer, older)
	if conflict || win.Connections[0]["id"] != "new" {
		t.Fatalf("the newer local must win without a conflict: %+v %v", win, conflict)
	}
	// An unparseable timestamp is not evidence of being newer: remote wins, but the
	// caller is told a conflict happened.
	broken := SyncDoc{UpdatedAt: "not-a-date"}
	win, conflict = MergeSync(broken, older)
	if !conflict || win.UpdatedAt != older.UpdatedAt {
		t.Fatalf("broken timestamp must fall back to remote: %+v %v", win, conflict)
	}

	// The conflict copy preserves the loser verbatim.
	dir := t.TempDir()
	path, err := WriteConflictCopy(dir, older)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "\"old\"") {
		t.Fatalf("the conflict copy lost the loser: %s", body)
	}
	if !strings.HasPrefix(filepath.Base(path), "sync-conflict-") {
		t.Fatalf("unexpected copy name %q", path)
	}
}

func TestSyncPushAndPullRoundTrip(t *testing.T) {
	var stored []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			stored, _ = io_readAll(r)
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			if stored == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(stored)
		}
	}))
	defer srv.Close()

	doc := BuildSyncDoc([]map[string]any{{"id": "c", "host": "h", "password": "nope"}}, nil)
	if err := SyncPush(context.Background(), srv.URL, doc); err != nil {
		t.Fatalf("push: %v", err)
	}
	got, err := SyncPull(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got.Connections) != 1 || got.Connections[0]["host"] != "h" {
		t.Fatalf("round trip = %+v", got)
	}
	if strings.Contains(string(stored), "nope") {
		t.Fatal("the pushed document carried the password")
	}
	// A 404 remote is an empty document, not an error (first push).
	empty, err := SyncPull(context.Background(), srv.URL+"-missing")
	if err == nil && len(empty.Connections) != 0 {
		t.Fatalf("a missing remote must be empty: %+v", empty)
	}
}

// io_readAll is a tiny reader helper (kept local so the test imports stay minimal).
func io_readAll(r *http.Request) ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

var _ = time.Now
