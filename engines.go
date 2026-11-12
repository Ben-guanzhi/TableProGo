package main

// Protocol-compatible engines: one wire protocol, one dialect, several product
// names. OceanBase's MySQL mode speaks the MySQL wire protocol, GreatSQL and
// StarRocks / Doris (FE query port) speak MySQL too, while openGauss, Huawei
// GaussDB and KingbaseES speak PostgreSQL's. The SQL dialect and catalog
// statements are shared through dbcore/sqltext.Normalize, so the client only
// needs to agree on which built-in driver opens the socket and which engine
// spelling the driver kind answers.

import (
	"strings"
)

// engineAliases maps a protocol-compatible engine spelling onto the built-in
// engine that serves it. The keys are exactly what ConnectionConfig.Type
// persists; the values are the built-in identifiers the rest of the code
// already branches on.
var engineAliases = map[string]string{
	"oceanbase":  "mysql", // MySQL 模式, 默认端口 2881
	"greatsql":   "mysql",
	"starrocks":  "mysql", // FE 查询端口 9030
	"doris":      "mysql", // FE 查询端口 9030
	"opengauss":  "postgresql",
	"gaussdb":    "postgresql",
	"kingbase":   "postgresql",
	"kingbasees": "postgresql",
}

// builtinEngine canonicalizes an engine spelling onto its built-in driver's
// identifier: aliases resolve to "mysql" / "postgresql", everything else comes
// back as written. sqlite / sqlserver / oracle / redis have no alias.
func builtinEngine(engineType string) string {
	if base, ok := engineAliases[strings.ToLower(strings.TrimSpace(engineType))]; ok {
		return base
	}
	return engineType
}

// unsupportedEngines names the engines this build RECOGNIZES but cannot open, and
// why. It is empty today — DM used to be listed here, and became a real engine in
// W14 once an Apache-2.0 driver was found — but the mechanism stays: the honest
// answer for an engine nobody can open is a named reason, not "unknown engine".
var unsupportedEngines = map[string]string{}

// unsupportedEngineReason returns why an engine cannot be opened, or "".
func unsupportedEngineReason(engineType string) string {
	return unsupportedEngines[strings.ToLower(strings.TrimSpace(engineType))]
}

// engineDefaultPort fills the port a new connection starts from. Aliases keep
// their own conventional ports (OceanBase 2881, StarRocks/Doris 9030); the
// PG-compatible engines reuse 5432.
func engineDefaultPort(engineType string) int {
	switch strings.ToLower(strings.TrimSpace(engineType)) {
	case "oceanbase":
		return 2881
	case "starrocks", "doris":
		return 9030
	case "opengauss", "gaussdb", "kingbase", "kingbasees":
		return 5432
	}
	return 0
}

// isMySQLLike reports whether the engine speaks the MySQL wire protocol. Every
// place that used to write conn.Type == "mysql" asks this instead, so
// OceanBase's MySQL mode and the other aliases branch the same way.
func isMySQLLike(engineType string) bool {
	return builtinEngine(engineType) == "mysql"
}

// isPostgresLike reports whether the engine speaks the PostgreSQL wire
// protocol (including openGauss / GaussDB / KingbaseES).
func isPostgresLike(engineType string) bool {
	return builtinEngine(engineType) == "postgresql"
}
