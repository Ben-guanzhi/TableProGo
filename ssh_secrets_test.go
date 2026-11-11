package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The SSH password is a credential: it must be sealed on disk exactly like the
// database password, and restored on load. Linux/macOS use the keyring or the
// documented plaintext fallback, so this assertion is Windows-only (DPAPI).
//
// The key passphrase is the third credential in the same struct and rides the
// same layer (ssh.go's contract). It was missing from both the seal and the
// unseal path, so it landed in the state file in clear text next to two sealed
// secrets — the same class of gap as the fynequery .bak leak.
func TestSSHPasswordSealedOnDisk(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI sealing is Windows-only; other platforms fall back to keyring/plaintext")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "connections.json")
	app := &App{
		filePath:    path,
		connections: map[string]*Connection{},
		savedConnections: []ConnectionConfig{{
			ID: "c1", Name: "tunneled", Type: "postgresql", Host: "db", Port: 5432,
			Username: "u", Password: "db-secret",
			SSH: SSHConfig{
				Enabled: true, Host: "bastion", Port: 22, User: "deploy",
				Password: "ssh-secret", KeyFile: "/keys/id_ed25519", Passphrase: "key-secret",
			},
		}},
	}
	app.saveToFile()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	for _, secret := range []string{"db-secret", "ssh-secret", "key-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%q written in clear text: %s", secret, raw)
		}
	}
	// The key path is not a secret and must stay readable.
	if !strings.Contains(string(raw), "/keys/id_ed25519") {
		t.Fatalf("the key path should survive: %s", raw)
	}

	loaded := &App{filePath: path, connections: map[string]*Connection{}}
	loaded.loadFromFile()
	if len(loaded.savedConnections) != 1 {
		t.Fatalf("loaded %d connections", len(loaded.savedConnections))
	}
	got := loaded.savedConnections[0]
	if got.Password != "db-secret" {
		t.Errorf("db password round-trip = %q", got.Password)
	}
	if got.SSH.Password != "ssh-secret" {
		t.Errorf("ssh password round-trip = %q", got.SSH.Password)
	}
	if got.SSH.Passphrase != "key-secret" {
		t.Errorf("ssh key passphrase round-trip = %q", got.SSH.Passphrase)
	}
	if !got.SSH.Enabled || got.SSH.User != "deploy" {
		t.Errorf("ssh settings lost: %+v", got.SSH)
	}
}

// Listings sent to the UI must not carry either credential.
func TestGetConnectionsBlanksSSHPassword(t *testing.T) {
	app := &App{
		connections: map[string]*Connection{},
		savedConnections: []ConnectionConfig{{
			ID: "c1", Name: "t", Type: "mysql", Password: "db-secret",
			SSH: SSHConfig{Enabled: true, Host: "bastion", Password: "ssh-secret"},
		}},
	}
	list := app.GetConnections()
	if len(list) != 1 {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].Password != "" || list[0].SSH.Password != "" {
		t.Fatalf("credentials leaked to the UI: %+v", list[0])
	}
	if list[0].SSH.Host != "bastion" {
		t.Fatalf("non-secret ssh fields should survive: %+v", list[0].SSH)
	}
}

// Editing a connection with a blank SSH password keeps the stored one.
func TestUpdateConnectionKeepsSSHPassword(t *testing.T) {
	app := &App{
		filePath:    filepath.Join(t.TempDir(), "connections.json"),
		connections: map[string]*Connection{},
		savedConnections: []ConnectionConfig{{
			ID: "c1", Name: "t", Type: "mysql", Password: "db-secret",
			SSH: SSHConfig{Enabled: true, Host: "bastion", Password: "ssh-secret"},
		}},
	}
	updated := ConnectionConfig{
		ID: "c1", Name: "t2", Type: "mysql", Password: "",
		SSH: SSHConfig{Enabled: true, Host: "bastion2", Password: ""},
	}
	if err := app.UpdateConnection(updated); err != nil {
		t.Fatalf("update: %v", err)
	}
	saved := app.savedConnections[0]
	if saved.Password != "db-secret" || saved.SSH.Password != "ssh-secret" {
		t.Fatalf("blank fields should preserve credentials: %+v", saved)
	}
	if saved.Name != "t2" || saved.SSH.Host != "bastion2" {
		t.Fatalf("non-secret fields should update: %+v", saved)
	}
}
