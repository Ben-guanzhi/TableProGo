# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Go + Wails desktop database client
- Multi-engine support: MySQL, PostgreSQL, SQLite, SQL Server, Oracle
- Data masking on every AI surface: string cells of the MCP `query` /
  `sample_table` results and of the LLM assistant's query output go through
  the shared dbcore/masking kernel (sensitive column names masked wholesale —
  email/phone/id_card/bank/password/token — plus PII shapes recognized inside
  free-text, keeping usable shapes like `a***@e**********.com` and
  `138****5678`). Results carry a `masked` flag; masking is server-side and
  not negotiable — the model sees data shapes, not a real person's PII.
- MCP tool `sample_table`: a read-only peek at a table's first N rows (default
  20, clamped to the server's MaxRows), so a model can grasp the data shape
  without hand-writing SQL. The table name must be a plain identifier —
  dialect-specific quoting is not available to the Catalog, so the injection
  surface is closed by shape; the LIMIT is built server-side and the existing
  single-statement and read-only gates still apply.
- Native-tool backup for SQLite: the official sqlite3 `.dump` (read-only URI
  open, tool found via TABLEPRO_SQLITE3 / TABULA_SQLITE3 or PATH) produces the
  BEGIN...COMMIT-wrapped executable SQL, alongside the existing pg_dump /
  mysqldump paths. Partial modes are refused honestly (.dump is whole-database)
  and an in-memory database is refused too — there is no file to hand the tool.
  The backup menu now appears for SQLite and the protocol-compatible engines,
  and the dialog names the tool and env var per engine family.
- Protocol-compatible engines in the connection dialog: OceanBase (MySQL mode,
  port 2881), GreatSQL, StarRocks and Apache Doris (FE query port 9030) speak
  the MySQL wire protocol; openGauss, Huawei GaussDB and KingbaseES speak
  PostgreSQL's. Each alias opens with the built-in driver of the engine whose
  protocol it speaks — the dialect, catalog statements, imports and native
  backups (pg_dump/mysqldump) come from the shared implementation, and the
  conventional port is prefilled per alias. The 18 `conn.Type == "mysql"` /
  `"postgresql"` comparisons now ask `isMySQLLike` / `isPostgresLike`
  (engines.go) instead, so an alias branches exactly like its base engine.
- TableProGo HTTP API backend
- Frontend with CodeMirror SQL editor
- Shared SQL generation library (tableprosql)
- Shared XLSX export library (tableproxlsx)
- Fyne query tool (fynequery)
- ERD foreign-key editing: add or drop a foreign key from the relationship
  diagram, with a preview of the generated DDL, the engine's own refusal when it
  cannot express the change, and a rollback statement for the applied edit.
  `POST /api/er/fk` renders under a `preview` flag and executes under the same
  Safe Mode pause as the structure editor.
- Structure-edit rollback: `POST /api/structure-edit/rollback` reverses a
  committed structure edit from its before-snapshot — restoring dropped columns,
  indexes and foreign keys, and undoing added or changed ones — and reports the
  engines whose reverse would need a table rebuild instead of emitting a
  statement they would reject.
- Import: Excel (XLSX/XLSM) reading — a workbook may hold several worksheets and
  each worksheet imports into its own table, so one bad sheet cannot take the
  others down (each sheet is its own transaction). Sheet names are cleaned into
  default table names. The pure-Go excelize reader shares the CSV/JSON
  `(cols, rows)` shape, so the existing transaction, skip-errors and upsert
  handling are reused unchanged.
- Column mapping on import: a non-empty map is authoritative (columns it does not
  name are skipped), it is applied before type inference and `CREATE TABLE`,
  renamed targets are rejected when duplicated, and a source column that is not
  in the file is refused before anything is written. The dialog supplies the map
  as text and reports it back as one entry per file column.
- Import preview: `POST /api/import/preview` resolves the mapping and renders the
  DDL and sample `INSERT` statements `ImportFile` would run, without touching the
  database. It shares the resolution path with the run, so the preview cannot
  drift from what is executed.
- Import: multiple files in one request (`paths`); SQL scripts are executed one
  after another and their results aggregated.

### Changed
- Dialect struct with function-based registry for SQL generation
- 8 dialect-specific functions centralized in dialect.go registry
- Keyset pagination enabled per dialect via KeysetEnabled flag
- The structure editor keeps its dialog open after a successful apply and offers
  "撤销" (undo) to run the reverse of the committed statements; closing it no
  longer discards the chance to roll the edit back.
- A structure-edit rollback now lands in the statement history like the forward
  edit does. The undo is a write against the same table — the audit trail must
  show both sides of every applied edit, or "who changed it and changed it
  back" has a gap.
- `ImportRequest` grew backward-compatible fields (`paths`, `sheet`, `sheets`,
  `columnMap`, `skipColumns`) rather than a new signature, so existing
  `/api/import` callers keep working; `.xlsx/.xlsm/.xls` are recognised as
  Excel by extension and read by the same import path as the other formats.

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
