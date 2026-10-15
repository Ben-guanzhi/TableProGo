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
- A whole number typed into a numeric cell or the filter bar is written as the
  digits it was typed instead of as a double. Past 2^53 the old path named a
  different number — and when the table happened to hold that neighbour, the row
  it edited was the neighbour's, with no error reported.

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
