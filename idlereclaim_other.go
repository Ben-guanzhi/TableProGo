package main

import (
	"log"
	"runtime/debug"
)

// debugFreeOSMemory returns unused memory to the OS. On all platforms this
// is the same call; it is factored out so tests can stub it.
func debugFreeOSMemory() {
	debug.FreeOSMemory()
}

// logReclaim records a reclaim event. Factored out for testability.
func logReclaim(bytes int64) {
	log.Printf("memory reclaim: collected %d bytes (idle, backend)", bytes)
}
