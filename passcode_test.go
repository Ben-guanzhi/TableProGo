package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newPasscodeApp(t *testing.T) *App {
	t.Helper()
	return &App{passcodePath: filepath.Join(t.TempDir(), "passcode.json")}
}

func TestPasscodeSetVerifyClear(t *testing.T) {
	a := newPasscodeApp(t)
	if a.HasPasscode() {
		t.Fatal("a fresh app reports a passcode it never set")
	}
	// A wrong candidate verifies false before any passcode exists — and the
	// store file is not created by a failed verify.
	if a.VerifyPasscode("guess") {
		t.Fatal("verify succeeded with no store")
	}
	if err := a.SetPasscode("hunter2"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !a.HasPasscode() {
		t.Fatal("HasPasscode false after set")
	}
	if !a.VerifyPasscode("hunter2") {
		t.Fatal("the stored passcode no longer verifies")
	}
	if a.VerifyPasscode("hunter3") {
		t.Fatal("a wrong passcode verified")
	}

	if err := a.ClearPasscode(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if a.HasPasscode() {
		t.Fatal("HasPasscode true after clear")
	}
	// Clearing twice is not an error: the lock is off either way.
	if err := a.ClearPasscode(); err != nil {
		t.Fatalf("second clear: %v", err)
	}
}

// The store holds a salted argon2id hash, never the passcode: reading the file
// must not give up what to type.
func TestPasscodeStoreHoldsNoPlaintext(t *testing.T) {
	a := newPasscodeApp(t)
	if err := a.SetPasscode("hunter2!"); err != nil {
		t.Fatalf("set: %v", err)
	}
	data, err := os.ReadFile(a.passcodeFile())
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(data), "hunter2") {
		t.Fatal("the store contains the plaintext passcode")
	}
	var s passcodeStore
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("store is not the documented shape: %v", err)
	}
	if s.Salt == "" || s.Hash == "" {
		t.Fatalf("empty store fields: %+v", s)
	}
}

// A short passcode is refused before anything is written.
func TestPasscodeMinimumLength(t *testing.T) {
	a := newPasscodeApp(t)
	if err := a.SetPasscode("123"); err == nil {
		t.Fatal("a 3-digit passcode was accepted")
	}
	if a.HasPasscode() {
		t.Fatal("the rejected set still created a store")
	}
}

// A truncated or hand-corrupted store verifies as "wrong passcode", not as a
// crash or a free pass.
func TestPasscodeCorruptStore(t *testing.T) {
	a := newPasscodeApp(t)
	if err := a.SetPasscode("hunter2"); err != nil {
		t.Fatalf("set: %v", err)
	}
	for _, body := range []string{"", "{", `{"salt":"!!!","hash":"###"}`, `{"salt":"","hash":""}`} {
		if err := os.WriteFile(a.passcodeFile(), []byte(body), 0o600); err != nil {
			t.Fatalf("corrupt: %v", err)
		}
		if a.VerifyPasscode("hunter2") {
			t.Fatalf("a corrupted store (%q) verified", body)
		}
	}
}

// Re-setting replaces the old passcode (the UI verifies first; that is its job).
func TestPasscodeOverwrite(t *testing.T) {
	a := newPasscodeApp(t)
	if err := a.SetPasscode("first-pass"); err != nil {
		t.Fatalf("set 1: %v", err)
	}
	if err := a.SetPasscode("second-pass"); err != nil {
		t.Fatalf("set 2: %v", err)
	}
	if a.VerifyPasscode("first-pass") {
		t.Fatal("the replaced passcode still verifies")
	}
	if !a.VerifyPasscode("second-pass") {
		t.Fatal("the new passcode does not verify")
	}
}
