package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func TestConnectionsFileIsSealedOnSave(t *testing.T) {
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
