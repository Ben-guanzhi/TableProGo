# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Go + Wails desktop database client
- Multi-engine support: MySQL, PostgreSQL, SQLite, SQL Server, Oracle
- TableProGo HTTP API backend
- Frontend with CodeMirror SQL editor
- Shared SQL generation library (tableprosql)
- Shared XLSX export library (tableproxlsx)
- Fyne query tool (fynequery)

### Changed
- Dialect struct with function-based registry for SQL generation
- 8 dialect-specific functions centralized in dialect.go registry
- Keyset pagination enabled per dialect via KeysetEnabled flag

### Fixed
- A number typed into a numeric cell or the filter bar is written as the digits
  it was typed instead of as a double. Past 2^53 the old path named a different
  number — and when the table happened to hold that neighbour, the row it edited
  was the neighbour's, with no error reported. A value with a decimal point has
  the same cliff one level down (`123456789012345678.90` came back as
  `123456789012345680`) and lost its scale besides (`1.50` went out as `1.5`),
  so the shape recognised now is an optional sign, digits, and at most one point
  with digits on both sides. `.5`, `1.` and `1e3` still take the path that has
  always rendered them; `0x10` and `1.2.3` are still refused.
- The MySQL index panel failed on every table. `tableprosql.IndexesQuery` selected
  four columns while `tableIndexes` scans five, so the read stopped at `rows.Scan`
  before a row arrived. The statement now answers the same five columns as the
  other engines do, and it derives the uniqueness flag instead of passing
  `NON_UNIQUE` through: that column names the opposite of what the readers scan
  it into, so a plain index was reported as UNIQUE. The caller's workaround of
  treating a key named PRIMARY as the missing flag is gone, since the statement
  says it. A test in the shared module pins the column count and the derived
  flags without needing a server, and the new `shared-retag` CI job fails when
  these modules hold changes past their tag - which is how the two clients came
  to run different SQL for the same catalog call in the first place.

## [0.1.0] - 2026-10-13

### Added
- Initial release
- Go + Wails 3 desktop application
- Shared SQL generation package (tableprosql)
- Shared XLSX export package (tableproxlsx)
- Fyne-based query tool (fynequery)

## [1.0.0] - 2026-10-13

### Changed
- Frontend version bump to 1.0.0
