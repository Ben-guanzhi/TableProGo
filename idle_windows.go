//go:build windows

package main

// OS-level idle seconds (P2-9). A locked or screensaver'd session keeps
// growing this counter, so an idle lock fires on the real thing instead of
// trusting webview activity — the webview stops getting mouse/keydown events
// exactly when the machine is locked, which is when a lock must NOT be
// skipped.
//
// The two Win32 facts db-dev paid for and that are re-pinned here:
//   - GetLastInputInfo lives in user32, GetTickCount64 lives in KERNEL32.
//     Looking the tick count up in user32 panics on some Windows images at
//     startup ("Failed to find GetTickCount64 procedure").
//   - GetTickCount64 is absent on ancient releases; the 32-bit GetTickCount is
//     the fallback, and its 49.7-day wrap is handled by wrapping subtraction.

import (
	"syscall"
	"time"
	"unsafe"
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	getLastInput   = user32.NewProc("GetLastInputInfo")
	getTickCount64 = kernel32.NewProc("GetTickCount64")
	getTickCount32 = kernel32.NewProc("GetTickCount")
)

// tickMillis returns milliseconds since boot: the 64-bit source when present,
// the wrapping 32-bit one otherwise.
func tickMillis() uint64 {
	if err := getTickCount64.Find(); err == nil {
		v, _, _ := getTickCount64.Call()
		return uint64(v)
	}
	v, _, _ := getTickCount32.Call()
	return uint64(uint32(v))
}

// systemIdleSeconds reports seconds since the last keyboard/mouse input.
func systemIdleSeconds() (int, error) {
	if err := getLastInput.Find(); err != nil {
		return 0, err
	}
	var li lastInputInfo
	li.cbSize = uint32(unsafe.Sizeof(li))
	ret, _, err := getLastInput.Call(uintptr(unsafe.Pointer(&li)))
	if ret == 0 {
		return 0, err
	}
	// Unsigned subtraction is correct across a tick-counter wrap: the result
	// is the distance, even when the counter rolled between the two reads.
	elapsed := uint32(tickMillis()) - li.dwTime
	return int(time.Duration(elapsed) * time.Millisecond / time.Second), nil
}
