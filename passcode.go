package main

// Local passcode (P2-9): the app can be locked behind a passcode stored as an
// argon2id hash, never as plaintext. Ported from db-dev's internal/security
// with the same shape: a 0600 JSON file in the user-data directory, atomic
// write, constant-time compare. The frontend keeps no hash of its own — the
// old djb2-in-localStorage idea is exactly what this replaces.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: 64 MB memory, 1 pass, 4 threads, 32-byte key. Heavy
// enough that a stolen passcode.json resists offline cracking, light enough
// that unlocking never feels like a stall.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16

	minPasscodeLen = 4
)

type passcodeStore struct {
	Salt string `json:"salt"` // base64(16B random salt)
	Hash string `json:"hash"` // base64(argon2id output)
}

func (a *App) passcodeFile() string { return a.passcodePath }

// HasPasscode reports whether a local passcode is set. The LOCK state is
// derived from this: an app with no passcode cannot be idle-locked (db-dev
// learned that locking without anything to unlock with only strands the user).
func (a *App) HasPasscode() bool {
	_, err := os.Stat(a.passcodeFile())
	return err == nil
}

// SetPasscode derives and stores the passcode hash (atomic write, 0600).
// Overwriting an existing passcode requires the old one to have just been
// verified by the caller (the UI flow), like any password change.
func (a *App) SetPasscode(passcode string) error {
	if len(passcode) < minPasscodeLen {
		return errors.New("口令至少 4 位")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash := argon2.IDKey([]byte(passcode), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	store := passcodeStore{
		Salt: base64.StdEncoding.EncodeToString(salt),
		Hash: base64.StdEncoding.EncodeToString(hash),
	}
	b, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	path := a.passcodeFile()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// VerifyPasscode checks a candidate against the stored hash in constant time.
// A missing file, a broken store or a wrong passcode all answer false: the
// lock never distinguishes "no passcode" from "wrong passcode" to a stranger.
func (a *App) VerifyPasscode(passcode string) bool {
	b, err := os.ReadFile(a.passcodeFile())
	if err != nil {
		return false
	}
	var s passcodeStore
	if err := json.Unmarshal(b, &s); err != nil {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(s.Salt)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(s.Hash)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(passcode), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ClearPasscode removes the local passcode store. Missing counts as cleared.
func (a *App) ClearPasscode() error {
	err := os.Remove(a.passcodeFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
