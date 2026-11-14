//go:build oracle

package main

// The Oracle driver is opt-in (波 100 对齐): go-ora alone adds ~13MB. Build
// with `-tags oracle` to register it; the default build answers an Oracle
// connection with a clear "unsupported database type" from the DSN switch.

import (
	_ "github.com/sijms/go-ora/v2" // registers "oracle"
)