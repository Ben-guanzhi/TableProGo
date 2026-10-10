export interface DatabaseFilter {
  // Any pattern makes the filter active; both lists empty shows everything.
  include: string[];
  exclude: string[];
}

export interface Connection {
  id: string;
  name: string;
  type: 'mysql' | 'postgresql' | 'sqlite' | 'sqlserver' | 'oracle';
  host: string;
  port: number;
  username: string;
  password?: string;
  database: string;
  sslMode: string;
  options: Record<string, string>;
  databaseFilter?: DatabaseFilter;
}

export interface ConnectionConfig {
  id?: string;
  name: string;
  type: 'mysql' | 'postgresql' | 'sqlite' | 'sqlserver' | 'oracle';
  host: string;
  port: number;
  username: string;
  password: string;
  database: string;
  sslMode: string;
  options: Record<string, string>;
  databaseFilter?: DatabaseFilter;
}

export interface Column {
  name: string;
  type: string;
  nullable: boolean;
}

export interface QueryResult {
  columns: Column[];
  rows: any[][];
  rowCount: number;
  affectedRows: number;
  lastInsertId: number;
  executionTime: number;
  error?: string;
  // Set when the row cap cut a SELECT short; more rows exist than shown.
  truncated?: boolean;
  // The key of this page's first and last row, as text. Handing one back asks
  // the server for the page beside this one without counting rows past.
  startCursor?: string[];
  endCursor?: string[];
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

export interface TableStructure {
  columns: ColumnInfo[];
  indexes: IndexInfo[];
  foreignKeys: ForeignKeyInfo[];
  createTable: string;
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
  // Object tabs browse one table instead of editing a statement; without a
  // kind a tab is a query tab.
  kind?: 'query' | 'table';
  tableRef?: TableRef;
  objectType?: 'table' | 'view';
  section?: ObjectSection;
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
