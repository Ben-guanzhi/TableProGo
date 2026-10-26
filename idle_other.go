//go:build !windows

package main

import "errors"

// systemIdleSeconds reports "not available": only the Windows build asks the
// OS. The frontend treats -1 as "fall back to in-page activity", so the idle
// lock still works (just without the locked-screen detection) elsewhere.
func systemIdleSeconds() (int, error) {
	return 0, errors.New("idle: not reported on this platform")
}
