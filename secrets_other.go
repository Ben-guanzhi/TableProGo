//go:build !windows

package main

import "fmt"

// protectSecret stores the password in the OS keyring (macOS Keychain, Linux
// Secret Service). A machine with no usable keyring falls back to the legacy
// plain-text store; the fallback and its warning live in protectViaKeyringOrPlain.
func protectSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return protectViaKeyringOrPlain(plaintext)
}

// unsealDPAPI refuses a Windows-sealed value: the key that opens it lives in
// another machine's DPAPI, so the password has to be retyped here.
func unsealDPAPI(stored string) (string, error) {
	return "", fmt.Errorf("password was sealed with Windows DPAPI; retype it")
}
