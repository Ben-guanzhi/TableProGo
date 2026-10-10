package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestSecretRoundTrip(t *testing.T) {
	const plaintext = "p@ss w0rd 中文"

	sealed, err := protectSecret(plaintext)
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(sealed, secretPrefix) {
			t.Fatalf("stored value is not marked as sealed: %q", sealed)
		}
		if strings.Contains(sealed, "p@ss") {
			t.Fatal("plaintext password leaked into the sealed value")
		}
	}

	opened, err := unprotectSecret(sealed)
	if err != nil {
		t.Fatalf("unprotect: %v", err)
	}
	if opened != plaintext {
		t.Fatalf("round trip lost the password")
	}
}

func TestSecretEmptyAndLegacyValues(t *testing.T) {
	if got, err := protectSecret(""); err != nil || got != "" {
		t.Fatalf("empty password must stay empty, got %q (%v)", got, err)
	}
	// Files written before encryption existed hold plain text and must still load.
	if got, err := unprotectSecret("legacy-plain"); err != nil || got != "legacy-plain" {
		t.Fatalf("legacy plain text should pass through, got %q (%v)", got, err)
	}
	if runtime.GOOS == "windows" {
		if _, err := unprotectSecret(secretPrefix + "not-valid-base64!!"); err == nil {
			t.Fatal("a corrupted sealed value should report an error")
		}
	}
}

// TestKeyringSealOpenRoundTrip pins the keyring half of the store on every
// platform: a sealed value is a "keyring:" reference that names no password,
// and the shared dispatcher fetches the password back out of the keyring.
func TestKeyringSealOpenRoundTrip(t *testing.T) {
	keyring.MockInit()

	sealed, err := sealViaKeyring("hunter2")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !strings.HasPrefix(sealed, keyringPrefix) {
		t.Fatalf("sealed value is not a keyring reference: %q", sealed)
	}
	if strings.Contains(sealed, "hunter2") {
		t.Fatal("the password leaked into its own keyring reference")
	}
	if !isSealedValue(sealed) {
		t.Fatal("a keyring reference must count as sealed")
	}

	opened, err := unprotectSecret(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened != "hunter2" {
		t.Fatalf("round trip lost the password, got %q", opened)
	}
}

// TestSecretsGC pins the save-time cleanup: entries the previous file pointed
// at that this save dropped are removed from the keyring, entries still
// referenced stay, and a reference to an already-missing entry is not an error.
func TestSecretsGC(t *testing.T) {
	keyring.MockInit()

	keep, err := sealViaKeyring("keep")
	if err != nil {
		t.Fatalf("seal keep: %v", err)
	}
	drop, err := sealViaKeyring("drop")
	if err != nil {
		t.Fatalf("seal drop: %v", err)
	}

	secretsGC([]string{keep, drop, "dpapi:x", "plain"}, []string{keep})

	if _, err := unprotectSecret(keep); err != nil {
		t.Fatalf("an entry still on disk was collected: %v", err)
	}
	if _, err := unprotectSecret(drop); err == nil {
		t.Fatal("the dropped entry should be gone from the keyring")
	} else if !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("want keyring not-found, got %v", err)
	}
	// GC of an id that is already gone must stay silent.
	dropID, _ := keyringRefID(drop)
	secretsGC([]string{keyringPrefix + dropID}, nil)
}

// TestProtectFallsBackWhenKeyringFails pins the documented last resort: a
// machine whose keyring refuses everything still saves the connection, with
// the password in clear text and a log line saying so.
func TestProtectFallsBackWhenKeyringFails(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))

	sealed, err := protectViaKeyringOrPlain("pw")
	if err != nil {
		t.Fatalf("fallback must not fail the save: %v", err)
	}
	if sealed != "pw" {
		t.Fatalf("fallback should keep the plain text, got %q", sealed)
	}
}

// TestKeyringWorksProbe pins the capability probe the Fyne dialog's warning
// rests on: a working keyring answers true, a broken one false.
func TestKeyringWorksProbe(t *testing.T) {
	keyring.MockInit()
	if !keyringWorks() {
		t.Fatal("a working keyring must probe true")
	}
	keyring.MockInitWithError(errors.New("no secret service"))
	if keyringWorks() {
		t.Fatal("a broken keyring must probe false")
	}
	keyring.MockInit()
}

func TestConnectionsFileIsSealedOnSave(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "connections.json")
	app := &App{
		filePath: path,
		savedConnections: []ConnectionConfig{
			{ID: "a", Name: "a", Password: "s3cret"},
			{ID: "b", Name: "b"},
		},
	}
	app.saveToFile()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Fatal("the saved file still holds the plain-text password")
	}

	reloaded := &App{filePath: path}
	reloaded.loadFromFile()
	if got := reloaded.savedConnections[0].Password; got != "s3cret" {
		t.Fatalf("reloaded password mismatch")
	}
	if got := reloaded.savedConnections[1].Password; got != "" {
		t.Fatalf("empty password became %q", got)
	}
}

// TestDeletedConnectionCollectsItsKeyringEntry pins the cleanup path the
// delete button rides on: dropping a connection from the file also drops its
// keyring entry on the next save, while a surviving connection's entry stays
// and its password still round-trips.
func TestDeletedConnectionCollectsItsKeyringEntry(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "connections.json")
	app := &App{
		filePath: path,
		savedConnections: []ConnectionConfig{
			{ID: "a", Name: "a", Password: "alpha"},
			{ID: "b", Name: "b", Password: "bravo"},
		},
	}
	app.saveToFile()

	// Capture what the file references per connection before the delete.
	before, ok, err := readJSONFile[ConnectionConfig](path)
	if err != nil || !ok || len(before) != 2 {
		t.Fatalf("saved file should hold 2 connections, got %d (%v, %v)", len(before), ok, err)
	}
	refOf := map[string]string{}
	for _, c := range before {
		refOf[c.ID] = c.Password
	}

	app.loadFromFile()
	if err := app.DeleteConnection("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// A deleted connection's keyring entry must be gone; a surviving one's
	// must stay. DPAPI values are re-sealed fresh on every save, so only the
	// keyring half of the store has a stable reference to assert on.
	if id, isKeyring := keyringRefID(refOf["a"]); isKeyring {
		if _, err := keyring.Get(keyringService, id); err == nil {
			t.Fatal("deleted connection's keyring entry survived the save")
		}
	}
	after, ok, err := readJSONFile[ConnectionConfig](path)
	if err != nil || !ok || len(after) != 1 {
		t.Fatalf("file should hold 1 connection after delete, got %d (%v, %v)", len(after), ok, err)
	}
	if id, isKeyring := keyringRefID(after[0].Password); isKeyring {
		if _, err := keyring.Get(keyringService, id); err != nil {
			t.Fatalf("surviving connection's keyring entry is gone: %v", err)
		}
	}

	reloaded := &App{filePath: path}
	reloaded.loadFromFile()
	if got := reloaded.savedConnections[0].Password; got != "bravo" {
		t.Fatalf("surviving password = %q, want bravo", got)
	}
}
