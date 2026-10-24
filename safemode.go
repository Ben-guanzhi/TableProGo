package main

// Safe Mode lives in internal/safemode so its classifier can be tested (and
// later reused) without dragging in the App wiring. These aliases keep the call
// sites in app.go/query.go/changes.go unchanged.

import "tablepro-go/internal/safemode"

const (
	safeModeOff            = safemode.Off
	safeModeConfirmWrites  = safemode.ConfirmWrites
	safeModeConfirmNoWhere = safemode.ConfirmNoWhere
	safeModeBlockNoWhere   = safemode.BlockNoWhere
)

func normalizeSafeMode(mode string) string { return safemode.Normalize(mode) }

type writeGuard = safemode.Verdict

func guardWrite(mode, query string) writeGuard { return safemode.Guard(mode, query) }

func hasWhereClause(query string) bool { return safemode.HasWhereClause(query) }
