# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0] - 2026-11-12

### Added
- Visual execution plan (W11, the EXPLAIN half of the Tabularis pair — the plan
  chose one of the two, and the graph is the cheaper of the two to get right):
  an EXPLAIN result gains a "可视化" entry that draws the plan as an SVG tree.
  The statement is asked for in the engine's machine-readable format
  (`sqltext.ExplainPlanStructured`: PostgreSQL `FORMAT JSON`, MySQL
  `FORMAT=JSON`, SQLite's `EXPLAIN QUERY PLAN` rows, SQL Server's
  `SHOWPLAN_XML`), so nothing is re-parsed out of prose. `internal/explainplan`
  parses each envelope (12 cases): PostgreSQL's nested nodes are BARE plan
  bodies while only the top level wraps in `{"Plan": …}` (declaring them wrapped
  parses every child as an empty object — the tree keeps its shape and loses
  every label); MySQL has no fixed schema so the walker treats any object with
  `table_name` as a leaf and recurses through unknown wrappers; SQL Server's
  `RelOp` tree is built with POINTERS because the scanned table arrives in a
  child `<Object>` element AFTER the node was attached (a value copy loses it);
  SQLite's parent links are the tree, and an orphan becomes a root rather than
  vanishing. Every engine's payload declares `utf-16` while the driver hands over
  UTF-8, so the XML decoder passes bytes through instead of failing on a charset
  Go has no reader for. A full scan is flagged for every engine (`SCAN t`,
  `Seq Scan`, `access_type=ALL`, `Clustered Index Scan`), because that is the one
  thing a plan viewer must point at. Oracle offers only `DBMS_XPLAN` prose, so it
  answers `format=text` and the grid stays: pretending to draw a plan we could
  only read as text would be worse than showing the text. The layout
  (`datagrid/planLayout.ts`, 8 cases) is deterministic — children in the plan's
  own order, positions as sums of fixed sizes, so two SVG exports of one plan
  agree — and a single-node plan is never magnified past 100%.
- Plugin trust gate and panel (W10): a plugin is an executable the user did not
  build, so its identity is checked BEFORE the launch, not after. Every declared
  plugin is hashed (SHA-256) at startup and lands in one of three states —
  `trusted` (matches what was vouched for) runs, `unknown` (nobody vouched for
  this file) runs and is surfaced, `tampered` (a vouched path whose bytes changed)
  is REFUSED. That asymmetry is the design: refusing `unknown` would break the
  existing `TABLEPRO_PLUGIN_<TYPE>=path` dev workflow, allowing `tampered` would
  make the trust store decorative. `internal/pluginreg` is the pure half (16
  cases): registry and trust store (atomic writes, BOM tolerant, unknown schema
  refused), path-keyed trust entries (absolute paths only — two spellings of one
  file must agree, two files must not collide), and a trust entry overriding a
  stale registry digest. `plugin_registry.json` ships a starter row on first open
  so the panel points at a file to edit instead of a blank list; `plugin_trust
  .json` records what the user vouched for, and the panel shows both digests side
  by side on a tamper. `TABLEPRO_HOME` now overrides the state directory (tests
  must not write into the real profile; portable installs want their state beside
  them) — without it the plugin tests were writing into `%APPDATA%\TableProGo`,
  and that residue was backed up and removed.
- Scheduled batch jobs (W7): `tablepro-go --job=<recipe.json>` runs a JSON recipe
  headless and exits — the shape that fits a "start on demand" app, because cron
  and Task Scheduler already own retry, log rotation and alerting. A recipe names
  saved connections BY ID and carries no credential, so it can live in a repo;
  steps are `backup` / `transfer` (W5) / `rowsync` (W6) / `wait`, run in order and
  stopping at the first failure. Exit codes are load-bearing: 0 all steps ran, 1 a
  step failed, 2 the recipe itself was unusable — the recipe is validated before
  any connection opens, so a typo alerts the person who wrote it rather than the
  person on call for the database. `--dry-run` validates and reports the plan
  (transfer and rowsync previews are read-only) without writing. A job has no UI
  to answer the app's confirm dialogs, so the recipe IS the confirmation — the
  risk surface is the file, not the run; `rowsync` defaults to analyse-only, and a
  plan exceeding the one-shot apply cap is refused rather than silently
  half-applied. Log lines are redacted (`password=`/`token=`/`secret=` → `***`)
  so a cron mail is safe to paste into a ticket. `docs/scheduled-jobs.md` carries
  the recipe reference plus cron, systemd timer and Task Scheduler examples.
- Row-level synchronization (W6): analyse a table's two sides by primary key and
  apply only the ticked differences. `internal/rowsync` is the pure merge (18
  cases): two streams ordered by the same key are merged with a two-way merge, so
  every key is visited exactly once and none is skipped; NULL equals NULL (both
  sides saying "no value" is agreement, not a difference), text comparison is
  byte-exact, and an unordered stream is refused rather than merged into a wrong
  plan. The orchestrator (`rowsync.go`, 10 end-to-end cases on real SQLite) reads
  both sides by the FULL primary key (a composite PK needs the whole tuple, which
  is why the fetcher sorts by every PK column and pages by offset), requires an
  identical column set and PK order — a mismatch names the columns instead of
  silently dropping them — and stores the plan so apply addresses changes by seq.
  Apply runs the ticked changes in ONE transaction (a half-applied sync is
  impossible) and requires `confirm`; an expired plan is refused. Tables without a
  primary key are refused outright: without a row identity there is nothing to
  address. The dialog lists every change with its operation, highlights deletions,
  and starts with NOTHING ticked — the cheap-looking default (tick all) is the
  destructive one.
- Server-to-server table copy (W5): preview then apply, with the plan visible
  before anything is written. `internal/transfer` is the pure planning half
  (17 cases): tables are ordered by FK dependency (Kahn's algorithm with a name
  tiebreak, so the order is a function of the input), a cycle is detected as the
  sort's residue and resolved by deferring those constraints until after every
  row is loaded, and cross-engine type losses are reported per column rather than
  coerced (integer widening is silent; decimal → float is not, because that is a
  decision). The orchestrator (`transfer.go`, 9 end-to-end cases on real SQLite)
  creates structure in plan order, copies each table's rows inside ONE
  transaction with batched parameterized INSERTs, reconciles read/write/target
  counts per table, and stops at the first failure with the table named. Apply
  re-derives the plan server-side and requires `confirm`, so a stale or forged
  preview cannot drive a copy. An engine without `ALTER TABLE ... ADD CONSTRAINT`
  (SQLite) gets the deferred constraints skipped WITH a warning rather than
  statements it would reject — the data still copies.
- Multi-cursor editing (W3-a): add a cursor above/below (`Ctrl/Cmd+Alt+↑↓`),
  select the next occurrence (`Ctrl/Cmd+D`) or every occurrence
  (`Ctrl/Cmd+Shift+D`), and `Esc` back to one cursor. CodeMirror 6 ships all four
  operations, so this is a binding exercise rather than the 588-line
  reimplementation db-dev carried.
- Staged-row colours (W3-b): a queued edit paints its row, derived from the same
  `ChangeQueue` the apply path reads (`datagrid/rowStatus.ts`, 9 cases) — a row
  can never be painted one colour and committed as another kind of change.
  Delete wins over update on a row carrying both, because the row is going away
  and an "updated" paint would promise a change that will never be saved; the
  toolbar's counts agree with the colours because both come from one summary.
- Shortcut table (W3-c): `lib/shortcuts.ts` holds every shortcut as data, and
  both the global key handler and the help panel read it — a shortcut can no
  longer be documented without being bound (the old list was hand-written beside
  the handler and could drift). Conflicts are a startup assertion rather than a
  silent last-wins, `normalizeCombo` is the single place that knows
  mod-is-Cmd-on-mac (with a literal `ctrl` kept distinct, because Ctrl+Space must
  stay Control on macOS), and documentation-only rows (mouse gestures, slash
  commands) carry no combo so they cannot claim a key they never consume.
- Dual-pane editor (W2-a, ported from the db-dev reference): a second editor
  beside the first, each holding its own statement, with one shared result grid.
  `Ctrl/Cmd+Enter` runs the pane that has focus; the active pane carries a thin
  accent bar on its top edge so "which one runs" is visible. The state is a
  small pure module (`dualPane.ts`, 9 cases): pane 1 is the historical `query`,
  so a tab that never opened the second pane behaves exactly as before, and
  leaving dual mode always lands back on pane 1. An empty active pane refuses to
  run instead of sending blank SQL.
- Vim keybindings in the SQL editor (W2-b): the modal engine is
  `@replit/codemirror-vim`, mounted in its own compartment so toggling the
  setting reconfigures the live editor instead of rebuilding it (undo history
  and the cursor survive the toggle). Off by default — Normal mode makes the
  editor feel broken to anyone who expected to type immediately.
- Format presets (W2-c): `default` (one clause per line, select items broken),
  `compact` (breaks only before FROM/WHERE/GROUP/ORDER/HAVING/SET, select list
  inline) and `expanded` (the default layout with four-space indents). All three
  share one tokenizer, so no preset can corrupt a literal or comment the others
  keep intact; an unknown or empty preset falls back to the default on both
  sides (`sqlformat.ParsePreset` / `normalizePreset`), so a settings typo can
  never stop formatting. `POST /api/format-sql` grew an optional `preset` field
  — existing callers keep the historical layout.
- Magic commands (W1-a, ported from the db-dev reference): a statement that
  begins with `\` is intercepted before it ever reaches the database and
  answered as a result grid. `\dt`/`\dv` list tables and views, `\dn` lists
  databases/schemas, `\d` describes a table's columns (defaulting to the
  table the tab shows), `\di` lists its indexes, `\df` lists routines, and
  `\copy <table> [format] [limit N]` exports a whole table to a downloaded
  file (csv/tsv/json/jsonl/markdown/xlsx) by paging the same preview endpoint
  the data card reads. The parser and executor are pure and unit-tested
  (`magicCommands.ts`, 23 cases); the fetchers ride the app's own HTTP API, so
  catalog reads stay on the shared paths. Unknown commands are refused with
  the help text, never executed. The shortcut help panel documents the set.
- Favorite folders can be renamed or dissolved (W1-b): a folder header gains
  two hover actions — rename moves every entry it holds to the new title
  (`RenameFavoriteFolder`, matched by the trimmed spelling the menu groups
  by), and 移出文件夹 returns them to 未分组 without deleting anything
  (`UnfileFavoriteFolder`). Both answer with the moved count for the toast;
  empty names and unknown folders are refused with the reason.
- TSV export (`toTSV`): the tab-separated sibling of the CSV builder, used by
  `\copy tsv` and available to any future exporter.

### Added
- Go + Wails desktop database client
- Multi-engine support: MySQL, PostgreSQL, SQLite, SQL Server, Oracle
- Protocol-compatible engines in the connection dialog: OceanBase (MySQL mode,
  port 2881), GreatSQL, StarRocks and Apache Doris (FE query port 9030) speak
  the MySQL wire protocol; openGauss, Huawei GaussDB and KingbaseES speak
  PostgreSQL's. Each alias opens with the built-in driver of the engine whose
  protocol it speaks — the dialect, catalog statements, imports and native
  backups (pg_dump/mysqldump) come from the shared implementation, and the
  conventional port is prefilled per alias. The 18 `conn.Type == "mysql"` /
  `"postgresql"` comparisons now ask `isMySQLLike` / `isPostgresLike`
  (engines.go) instead, so an alias branches exactly like its base engine.
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
- Native-tool backup for SQLite: the official sqlite3 `.dump` (read-only URI
  open, tool found via TABLEPRO_SQLITE3 / TABULA_SQLITE3 or PATH) produces the
  BEGIN...COMMIT-wrapped executable SQL, alongside the existing pg_dump /
  mysqldump paths. Partial modes are refused honestly (.dump is whole-database)
  and an in-memory database is refused too — there is no file to hand the tool.
  The backup menu now appears for SQLite and the protocol-compatible engines,
  and the dialog names the tool and env var per engine family.
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
