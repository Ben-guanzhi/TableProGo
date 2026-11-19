// Package sqlrisk delegates to the kernel's sqlrisk: the classification logic,
// the Chinese copy and the literal/comment scans moved to dbcore/sqlrisk
// (§343), and this package keeps the import path TableProGo's callers use.
package sqlrisk

import (
	kernel "github.com/Ben-guanzhi/dbcore/sqlrisk"
)

type (
	Level = kernel.Level
	Risk  = kernel.Risk
)

const (
	LevelRead        = kernel.LevelRead
	LevelWrite       = kernel.LevelWrite
	LevelDestructive = kernel.LevelDestructive
)

var Assess = kernel.Assess
