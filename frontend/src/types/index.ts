export interface DatabaseFilter {
  // Any pattern makes the filter active; both lists empty shows everything.
  include: string[];
  exclude: string[];
}

export interface Connection {
  id: string;
  name: string;
  type: 'mysql' | 'postgresql' | 'sqlite' | 'sqlserver' | 'oracle' | 'redis';
  host: string;
  port: number;
  username: string;
  password?: string;
  database: string;
  sslMode: string;
  options: Record<string, string>;
  ssh?: SSHConfig;
  databaseFilter?: DatabaseFilter;
}

export interface SSHConfig {
  enabled: boolean;
  host: string;
  port: number;
  user: string;
  password: string;
  keyFile: string;
  passphrase: string;
}

export interface ConnectionConfig {
  id?: string;
  name: string;
  type: 'mysql' | 'postgresql' | 'sqlite' | 'sqlserver' | 'oracle' | 'redis';
  host: string;
  port: number;
  username: string;
  password: string;
  database: string;
  sslMode: string;
  options: Record<string, string>;
  ssh?: SSHConfig;
  databaseFilter?: DatabaseFilter;
}

export interface Column {
  name: string;
  type: string;
  nullable: boolean;
  // Exact-decimal column: its cells cross as digit text and sort as numbers.
  isDecimal?: boolean;
}

export interface QueryResult {
  columns: Column[];
  rows: any[][];
  rowCount: number;
  affectedRows: number;
  lastInsertId: number;
  executionTime: number;
  error?: string;
  // Set when Safe Mode paused the statement: nothing ran, error explains why.
  needsConfirmation?: boolean;
  // Set when the row cap cut a SELECT short; more rows exist than shown.
  truncated?: boolean;
  // The key of this page's first and last row, as text. Handing one back asks
  // the server for the page beside this one without counting rows past.
  startCursor?: string[];
  endCursor?: string[];
}

// One key opened from the navigator: identity, remaining lifetime and the
// rows describing its value. The rows reuse QueryResult so the grid renders
// them without a second model.
export interface KVValue {
  key: string;
  type: string;
  ttl: string;
  result: QueryResult;
  // P3-3 windowing: `next` is the opaque cursor of the following window and
  // `done` says no more exist. Absent next on a whole-key read means done.
  next?: string;
  done?: boolean;
}

// A key's identity without its value, read before the right-click menu offers
// TTL or rename.
export interface KVKeyInfo {
  key: string;
  type: string;
  ttl: string;
}

// One SCAN step of a logical database. The cursor is a string because Redis
// cursors are uint64 and JSON numbers round past 2^53.
export interface KVKeyPage {
  cursor: string;
  keys: string[];
  done: boolean;
}

export interface KVField {
  name: string;
  value: string;
}

// One row appended to a set or stream. Both keys add an identity (SADD/XADD)
// instead of overwriting a cell, so this is not a KVElementInput.
export interface KVRowInput {
  connId: string;
  database: string;
  key: string;
  kind: 'set' | 'stream';
  member?: string;
  id?: string;
  fields?: KVField[];
}

export interface TableInfo {
  name: string;
  schema: string;
  type: 'table' | 'view';
  comment: string;
}

export interface ColumnInfo {
  name: string;
  type: string;
  nullable: boolean;
  defaultValue?: string;
  isPrimaryKey: boolean;
  comment: string;
}

export interface IndexInfo {
  name: string;
  kind: string;
  unique: boolean;
  primary: boolean;
  columns: string[];
  // method is the engine's access method (btree / BTREE / NONCLUSTERED / …).
  method?: string;
  // parts is the ordered key list for display: a column name or an expression
  // text, with " DESC" for a descending part. columns keeps plain names only.
  parts?: string[];
  // predicate is a partial/filtered index's WHERE clause.
  predicate?: string;
}

export interface ForeignKeyInfo {
  name: string;
  columns: string[];
  refSchema: string;
  refTable: string;
  refColumns: string[];
  onDelete: string;
  onUpdate: string;
}

// One attribute of a table in the relationship (ER) diagram. Carries only what
// the SVG node needs: the name, the engine type and whether it anchors the PK.
export interface ERColumn {
  name: string;
  type: string;
  nullable: boolean;
  isPrimaryKey: boolean;
}

// One foreign key of a table, pointing at another table in the same diagram.
export interface ERFK {
  name: string;
  columns: string[];
  refSchema: string;
  refTable: string;
  refColumns: string[];
  onDelete: string;
  onUpdate: string;
}

// One node of the diagram: its columns and the foreign keys leaving it.
export interface ERTable {
  name: string;
  schema: string;
  columns: ERColumn[];
  foreignKeys: ERFK[];
}

// The whole diagram: every base table of one schema plus the relationships
// between them. Returned by the backend and laid out as an SVG by the frontend.
export interface ERSchema {
  tables: ERTable[];
}

export interface TriggerInfo {
  name: string;
  timing: string;
  event: string;
  enabled: string;
}

export interface TableStructure {
  columns: ColumnInfo[];
  indexes: IndexInfo[];
  foreignKeys: ForeignKeyInfo[];
  triggers?: TriggerInfo[];
  // checks lists CHECK constraints; empty when the engine has none or the read
  // is unsupported (SQLite has no constraint catalog).
  checks?: CheckInfo[];
  // partitions lists a partitioned table's partitions; empty for a plain table
  // and for engines without partitioning.
  partitions?: PartitionInfo[];
  // viewDefinition is the view's CREATE DDL (view tabs only).
  viewDefinition?: string;
  createTable: string;
}

export interface CheckInfo {
  name: string;
  expression: string;
}

// One partition of a partitioned table: the engine's label, its boundary text
// and an approximate row count (-1 when unknown).
export interface PartitionInfo {
  name: string;
  bound: string;
  rows: number;
}

// One user-defined enum type and its labels in declaration order (PostgreSQL).
export interface EnumTypeInfo {
  name: string;
  labels: string[];
}

// One stored function or procedure in the schema-level list.
export interface RoutineInfo {
  name: string;
  kind: string; // FUNCTION | PROCEDURE
}

export interface TableRef {
  connId: string;
  database: string;
  schema: string;
  table: string;
}

// One condition of the data card's filter bar. It names a column and a
// comparison only - the SQL is rebuilt by the backend from the table's own
// catalog, so nothing typed in the bar can reach the database as a statement.
export interface SqlFilter {
  column: string;
  op: SqlFilterOp;
  value: string;
}

// The key edges a data page can be stepped off from. `after` asks for the page
// after them, `before` for the page before. A page read by row count instead
// needs neither.
export type PreviewCursor = { after?: string[]; before?: string[] };

export type SqlFilterOp =
  | 'eq'
  | 'ne'
  | 'gt'
  | 'gte'
  | 'lt'
  | 'lte'
  | 'contains'
  | 'null'
  | 'notNull';

// One recorded statement, newest first in whatever list the backend returns.
export interface HistoryEntry {
  id: number;
  // Unix milliseconds, as stored by the backend.
  time: number;
  connId: string;
  database: string;
  table?: string;
  sql: string;
  rowCount: number;
  affectedRows: number;
  executionTime: number;
  error?: string;
}

// One value typed into the grid. The column is named as the result named it.
export interface CellEdit {
  column: string;
  value: unknown;
}

// A new row carries no kind: the builder reads an omitted kind as an update of
// one existing row, which is what an edited cell is.
export type RowKind = 'insert' | 'delete';

// A row as the grid read it, plus what changed in it. The original values are
// what the key columns are taken from.
export interface RowChange {
  kind?: RowKind;
  original: unknown[];
  edits: CellEdit[];
}

export interface ChangeSet {
  connId: string;
  database: string;
  schema: string;
  table: string;
  columns: string[];
  changes: RowChange[];
}

// One paginated card of an object tab: the rows, or the structure read from
// the catalog.
export interface SectionState<T> {
  loading: boolean;
  data?: T;
  error?: string;
}

// ---- schema comparison / synchronization ----

// One side of a comparison: a connection plus the database and schema in it.
export interface CompareTarget {
  connId: string;
  database: string;
  schema: string;
}

export interface CompareOptions {
  ignoreDefaults: boolean;
  ignoreNullability: boolean;
  ignoreIndexes: boolean;
  ignoreForeignKeys: boolean;
}

// One change the plan proposes. `selected` is the initial tick: additive
// changes start ticked, destructive ones do not.
export interface CompareEntry {
  id: string;
  op: string;
  table: string;
  object: string;
  from: string;
  to: string;
  phase: number;
  reason: string;
  destructive: boolean;
  selected: boolean;
}

export interface CompareStatement {
  entryId: string;
  sql: string;
}

export interface CompareResult {
  planId: string;
  sourceLabel: string;
  targetLabel: string;
  kind: string;
  schema: string;
  entries: CompareEntry[];
  statements: CompareStatement[];
  warnings: string[];
}

export interface ApplySyncFailure {
  entryId: string;
  sql: string;
  error: string;
}

export interface ApplySyncResult {
  applied: number;
  total: number;
  failures: ApplySyncFailure[];
}

export type ObjectSection = 'data' | 'structure' | 'query' | 'history';

export interface Tab {
  id: string;
  title: string;
  query: string;
  // Database the query runs against; empty means the connection's own.
  database?: string;
  // True while the statement is in flight, so the grid does not show the
  // previous run's rows as if they were the new ones.
  loading?: boolean;
  result?: QueryResult;
  // Which table the rows came from, when they can be written back to one.
  source?: TableRef | null;
  // Column comments keyed by lower-cased column name, from the query's table.
  comments?: Record<string, string>;
  isDirty: boolean;
  connectionId?: string;
  // Object tabs browse one table instead of editing a statement; a kv tab
  // shows one Redis key read-only. Without a kind a tab is a query tab.
  kind?: 'query' | 'table' | 'kv' | 'er' | 'compare';
  tableRef?: TableRef;
  kv?: SectionState<KVValue>;
  objectType?: 'table' | 'view';
  section?: ObjectSection;
  // A compare tab starts from the connection the navigator was on; the view lets
  // the user change both sides from there.
  compareSource?: CompareTarget;
  // An open interactive transaction, as the backend describes it ("事务中 · 2 条语句 ·
  // app"). Empty means the tab runs in autocommit.
  txStatus?: string;
  data?: SectionState<QueryResult>;
  // One page of the data card: rows are read by key when the page has one to
  // step from, and by OFFSET otherwise.
  dataPage?: number;
  dataPageSize?: number;
  // Which way the page on screen was stepped to, and the key edges it holds:
  // the request cursor the next step uses, and the edges of the page shown.
  dataRequest?: PreviewCursor;
  dataEdges?: { start?: string[]; end?: string[] };
  // Conditions the data card filters by on the server, re-reading the page.
  dataFilters?: SqlFilter[];
  structure?: SectionState<TableStructure>;
  history?: SectionState<HistoryEntry[]>;
  // The object tab's own query page: its statement, its result and the
  // comments of the table it reads, kept apart from the data card.
  objectQuery?: string;
  objectResult?: SectionState<QueryResult>;
  objectComments?: Record<string, string>;
  // Which table the query page's rows can be written back to.
  objectSource?: TableRef | null;
}
