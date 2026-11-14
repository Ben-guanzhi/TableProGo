package main

// Driver registration, in one place. database/sql's registry is global, so a
// single blank import per process is all it takes — the copies that used to
// sit at the top of eight files were paste noise, not separate registrations.
//
// 波 100 对齐（dbstudio 先例）：Oracle 的纯 Go 驱动 (go-ora) costs ~13MB, so it
// is opt-in — build with `-tags oracle`; the default build serves MySQL /
// PostgreSQL / SQLite. SQL Server 与达梦 (dameng) 由 connSpecAt 的 DSN 分支
// 覆盖，其驱动注册在 dbcore 的对应包（drivers_dm.go 等，随用随引）。

import (
	_ "github.com/go-sql-driver/mysql" // registers "mysql"
	_ "github.com/lib/pq"              // registers "postgres"
	_ "github.com/mattn/go-sqlite3"    // registers "sqlite3" (cgo; keep CGO_ENABLED=1)
)