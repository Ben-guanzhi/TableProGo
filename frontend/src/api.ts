import type {
  ApplySyncResult,
  ChangeSet,
  Column,
  ColumnInfo,
  CompareOptions,
  CompareResult,
  CompareTarget,
  Connection,
  ConnectionConfig,
  ERSchema,
  ForeignKeyInfo,
  HistoryEntry,
  KVKeyInfo,
  KVKeyPage,
  KVRowInput,
  KVValue,
  PreviewCursor,
  QueryResult,
  RoutineInfo,
  SqlFilter,
  TableInfo,
  TableStructure,
} from './types/index.ts';

// Injected by the Go asset handler; the ?apiPort=/?apiToken= fallbacks keep the
// vite dev server usable from a plain browser. The token is minted per process
// and printed to the log line above "HTTP API server on port".
declare global {
  interface Window {
    __API_PORT__?: string;
    __API_TOKEN__?: string;
  }
}
const API_PORT = window.__API_PORT__
  ?? new URLSearchParams(window.location.search).get('apiPort');
const API_TOKEN = window.__API_TOKEN__
  ?? new URLSearchParams(window.location.search).get('apiToken') ?? '';
const BASE = `http://127.0.0.1:${API_PORT}`;

export async function post(path: string, body: any = {}): Promise<any> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-API-Token': API_TOKEN },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  // Endpoints answer `null` for an empty catalog (a sqlite connection has no
  // schemas), which is a result, not an error.
  if (data?.error) throw new Error(data.error);
  return data;
}

async function get(path: string): Promise<any> {
  const res = await fetch(`${BASE}${path}`, { headers: { 'X-API-Token': API_TOKEN } });
  const data = await res.json();
  if (data?.error) throw new Error(data.error);
  return data;
}

export async function GetConnections(): Promise<Connection[]> {
  return get('/api/connections');
}

export async function AddConnection(config: ConnectionConfig): Promise<void> {
  await post('/api/connections', config);
}

export async function UpdateConnection(config: ConnectionConfig): Promise<void> {
  await post('/api/connections/update', config);
}

export async function DeleteConnection(id: string): Promise<void> {
  await post('/api/connections/delete', { id });
}

export async function TestConnection(config: ConnectionConfig): Promise<void> {
  await post('/api/test-connection', config);
}

export async function Connect(id: string): Promise<void> {
  await post('/api/connect', { id });
}

export async function Disconnect(id: string): Promise<void> {
  await post('/api/disconnect', { id });
}

export async function GetDatabases(connId: string): Promise<string[]> {
  return post('/api/databases', { connId });
}

export async function GetTables(connId: string, database: string, schema = ''): Promise<TableInfo[]> {
  return post('/api/tables', { connId, database, schema });
}

export async function GetSchemas(connId: string, database: string): Promise<string[]> {
  return post('/api/schemas', { connId, database });
}

export async function GetColumns(connId: string, database: string, schema: string, table: string): Promise<ColumnInfo[]> {
  return post('/api/columns', { connId, database, schema, table });
}

// One Redis key read whole. The type and remaining lifetime sit beside the
// rows so the panel can name what it is showing.
export interface BackupRequest {
  connId: string;
  database: string;
  mode: 'full' | 'structure' | 'data';
  tables: string[];
  outPath: string;
}

export interface BackupResult {
  ok: boolean;
  log: string[];
  error?: string;
}

export async function BackupDatabase(req: BackupRequest): Promise<BackupResult> {
  return post('/api/backup', req);
}

export interface Favorite {
  id: string;
  connId: string;
  database: string;
  name: string;
  sql: string;
  createdAt: number;
}

export async function ListFavorites(): Promise<Favorite[]> {
  return (await get('/api/favorites')) as Favorite[];
}

export async function AddFavorite(
  connId: string,
  database: string,
  name: string,
  sql: string,
): Promise<Favorite> {
  return post('/api/favorites', { connId, database, name, sql });
}

export async function DeleteFavorite(id: string): Promise<void> {
  await post('/api/favorites/delete', { id });
}

// Pretty-print SQL: keywords upper-cased, one clause per line. The rules live
// in the Go formatter (internal/sqlformat), so every client shares one style.
export async function FormatSQL(sql: string): Promise<string> {
  const data = await post('/api/format-sql', { sql });
  return data.sql ?? sql;
}

// One-shot security notice (e.g. the OS keyring fell back to plain text).
// Empty string means nothing to tell the user.
export async function SecurityNotice(): Promise<string> {
  const data = await get('/api/security-notice');
  return (data as { notice?: string }).notice ?? '';
}

export async function GetKVValue(connId: string, database: string, key: string): Promise<KVValue> {
  return post('/api/kv-value', { connId, database, key });
}

// One SCAN step. An empty cursor starts at the beginning; `done` means the walk
// reached cursor 0. Keys can repeat across pages, so callers de-duplicate.
export async function GetKVKeys(
  connId: string,
  database: string,
  cursor = '',
  count = 0,
): Promise<KVKeyPage> {
  return post('/api/kv-keys', { connId, database, cursor, count });
}

export async function GetKVKeyInfo(connId: string, database: string, key: string): Promise<KVKeyInfo> {
  return post('/api/kv-key-info', { connId, database, key });
}

export async function SetKVKeyTTL(
  connId: string,
  database: string,
  key: string,
  seconds: number,
): Promise<void> {
  await post('/api/kv-key-ttl', { connId, database, key, seconds });
}

export async function RenameKVKey(
  connId: string,
  database: string,
  key: string,
  newName: string,
): Promise<void> {
  await post('/api/kv-key-rename', { connId, database, key, newName });
}

export async function DeleteKVKey(connId: string, database: string, key: string): Promise<void> {
  await post('/api/kv-key-delete', { connId, database, key });
}

// One element rewritten in place. `identity` follows the value's grid layout:
// key (a string), field (hash), index (list) or member (a sorted set's score).
// The backend keeps the key's TTL.
export interface KVElementInput {
  connId: string;
  database: string;
  key: string;
  identity: 'key' | 'field' | 'index' | 'member';
  id: string;
  column: 'value' | 'score';
  value: string;
}

export async function SetKVElement(input: KVElementInput): Promise<void> {
  await post('/api/kv-element', input);
}

// One row appended to a set member list or a stream. The response carries the
// identity the server assigned: the member, or the generated stream ID.
export async function AddKVRow(input: KVRowInput): Promise<{ id: string }> {
  return post('/api/kv-row-add', input);
}

// The key edges of the page currently on screen. `after` walks to the next page,
// `before` to the previous one; the page is read by row count again when they
// are missing, when the object has no key, or when a cursor no longer fits it.
export async function PreviewTable(
  ref: TableRef,
  limit = 100,
  offset = 0,
  filters: SqlFilter[] = [],
  cursor: PreviewCursor = {},
): Promise<any> {
  return post('/api/preview', {
    ...ref,
    limit,
    offset,
    filters,
    after: cursor.after,
    before: cursor.before,
  });
}

export type TableDdlAction = 'drop' | 'truncate' | 'rename';

// Rename/truncate/drop one object. The backend renders the statement from the
// names in its own catalog - and refuses anything that is not there - so no
// text typed in the menu can reach the database as SQL. When Safe Mode pauses
// the statement, one blocking confirmation is collected and the action is
// resent through the confirmed endpoint; cancelling throws, so the caller
// treats it like any other aborted action.
export async function RunTableAction(
  ref: TableRef,
  action: TableDdlAction,
  newName = '',
  confirm: (message: string) => boolean = message => window.confirm(message),
): Promise<string> {
  const data = await post('/api/table-action', { ...ref, action, newName });
  if (data.needsConfirmation) {
    const reason: string = data.reason ?? '';
    if (!confirm(`${reason}\n\n仍要执行这条语句吗？`)) {
      throw new Error('Cancelled (Safe Mode)');
    }
    const confirmed = await post('/api/table-action-confirmed', { ...ref, action, newName });
    return confirmed.statement ?? '';
  }
  return data.statement ?? '';
}

export async function GetTriggerDefinition(
  connId: string,
  database: string,
  schema: string,
  trigger: string,
): Promise<string> {
  const data = await post('/api/trigger-definition', { connId, database, schema, table: trigger });
  return data.definition ?? '';
}

// The schema's stored functions and procedures (P3-4 ExtraMetadataReader).
// Engines without the concept answer an error with the reason, not an empty list.
export async function GetRoutines(
  connId: string,
  database: string,
  schema: string,
): Promise<RoutineInfo[]> {
  return post('/api/routines', { connId, database, schema });
}

export async function GetRoutineDefinition(
  connId: string,
  database: string,
  schema: string,
  name: string,
): Promise<string> {
  const data = await post('/api/routine-definition', { connId, database, schema, name });
  return data.definition ?? '';
}

export interface ColumnEdit {
  name: string;
  type: string;
  nullable: boolean;
  defaultValue: string;
  isNew?: boolean;
}

export interface StructureEditResult {
  statements: string[];
  warnings: string[];
}

export async function PreviewStructureEdit(req: {
  connId: string;
  database: string;
  schema: string;
  table: string;
  columns: ColumnEdit[];
}): Promise<StructureEditResult> {
  return post('/api/structure-preview', req);
}

export async function ApplyStructureEdit(req: {
  connId: string;
  database: string;
  schema: string;
  table: string;
  columns: ColumnEdit[];
}): Promise<StructureEditResult> {
  return post('/api/structure-edit', req);
}

// objectType tells the backend whether to read a view definition or the
// table-only extras (CHECK constraints), so one structure endpoint serves both.
export async function GetTableStructure(
  connId: string,
  database: string,
  schema: string,
  table: string,
  objectType?: 'table' | 'view',
): Promise<TableStructure> {
  return post('/api/structure', { connId, database, schema, table, type: objectType });
}

// One schema's relationship diagram: every base table with its columns and
// foreign keys. Read-only, so the frontend lays it out as an SVG without ever
// sending a statement to the database.
export async function GetERDiagram(connId: string, database: string, schema: string): Promise<ERSchema> {
  return post('/api/er', { connId, database, schema });
}

// Compare two schemas and return the plan that would make the target look like
// the source. Read-only: nothing runs until ApplySync is called with the plan id
// this call returned.
export async function CompareSchemas(
  source: CompareTarget,
  target: CompareTarget,
  options: CompareOptions,
): Promise<CompareResult> {
  return post('/api/compare', { source, target, options });
}

// Run the selected entries of a plan. The request carries the plan id and entry
// ids, never SQL — the backend renders the statements from the plan it stored.
export async function ApplySync(planId: string, entryIds: string[]): Promise<ApplySyncResult> {
  return post('/api/compare/apply', { planId, entryIds, confirm: true });
}

export interface TableRef {
  connId: string;
  database: string;
  schema: string;
  table: string;
}

// The properly quoted SQL spelling of a table reference. The backend owns the
// dialect table (tableprosql), so editor templates never hand-build identifiers.
export async function SqlName(ref: TableRef, scope: 'local' | 'qualified'): Promise<string> {
  const data = await post('/api/sql-name', { ...ref, scope });
  return data.name ?? '';
}

// One choice for a foreign-key cell: the key it would store, plus a short
// description of the row behind it.
export interface ForeignKeyValue {
  value: string;
  label: string;
}

export interface ForeignKeyCandidates {
  refTable: string;
  refColumn: string;
  labelColumn: string;
  values: ForeignKeyValue[];
  truncated: boolean;
}

// Values the referenced table holds for this column. The backend resolves the
// constraint from its own catalog and refuses a column that has none, so a
// composite key never turns into a half-written cell. The caller caches the
// answer per column, so a table opened twice reads once.
export async function GetForeignKeyValues(
  ref: TableRef,
  column: string,
  limit = 200,
): Promise<ForeignKeyCandidates> {
  return post('/api/fk-values', { ...ref, column, limit });
}

// The constraints on a table, so a cell knows whether it holds a reference
// before anyone opens a list. Read once per editable source.
export async function GetTableForeignKeys(ref: TableRef): Promise<ForeignKeyInfo[]> {
  return ((await post('/api/foreign-keys', { ...ref })) as ForeignKeyInfo[]) ?? [];
}

// INSERT statements that rebuild the given rows, rendered by the backend with
// the same literal rules a save uses. It refuses anything it cannot trace back
// to a column of the table, so an alias never becomes a wrong script.
export async function ExportInsertStatements(
  ref: TableRef,
  columns: string[],
  rows: any[][],
): Promise<string> {
  const data = await post('/api/export-insert', { ...ref, columns, rows });
  return data.sql ?? '';
}

// The workbook is built by the backend, which is where the column types came
// from, so the cells can be numbers and dates rather than text. The local API
// only carries JSON, so the file comes back base64 encoded.
const XLSX_MIME = 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet';

export async function ExportWorkbook(
  columns: { name: string; type: string }[],
  rows: any[][],
): Promise<Blob> {
  const data = await post('/api/export-xlsx', { columns, rows });
  const encoded: string = data.base64 ?? '';
  const binary = atob(encoded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return new Blob([bytes], { type: XLSX_MIME });
}

// One catalog hit from the name search behind the quick-open box.
export interface ObjectMatch extends TableRef {
  type: 'table' | 'view';
}

// Tables and views whose name contains the fragment, across every connected
// connection; the server walks the catalogs, so nothing needs to be open first.
export async function SearchObjects(pattern: string, limit = 50): Promise<ObjectMatch[]> {
  return ((await post('/api/search-objects', { pattern, limit })) as ObjectMatch[]) ?? [];
}

export interface HistoryFilter {
  connId?: string;
  database?: string;
  table?: string;
  limit?: number;
}

// Statements the backend recorded for every execution, newest first.
export async function GetHistory(filter: HistoryFilter): Promise<HistoryEntry[]> {
  return post('/api/history', filter);
}

export async function ClearHistory(filter: HistoryFilter): Promise<number> {
  const data = await post('/api/clear-history', filter);
  return data.removed;
}

export async function ExecuteQuery(connId: string, query: string, database = ''): Promise<QueryResult> {
  return post('/api/execute', { connId, query, database });
}

// ExecuteQueryConfirmed re-sends a statement the Safe Mode prompt paused.
export async function ExecuteQueryConfirmed(connId: string, query: string, database = ''): Promise<QueryResult> {
  return post('/api/execute-confirmed', { connId, query, database });
}

// Interactive transactions (backend txn.go): one per tab, so two editor tabs on one
// connection keep separate transactions. The statements themselves go through
// ExecuteQueryInTab, which joins the tab's open transaction.
export async function BeginTransaction(connId: string, tabId: string, database = ''): Promise<string> {
  const res = await post('/api/begin-tx', { connId, tabId, database });
  return (res as { status: string }).status;
}

export async function CommitTransaction(connId: string, tabId: string): Promise<string> {
  const res = await post('/api/commit-tx', { connId, tabId, database: '' });
  return (res as { status: string }).status;
}

export async function RollbackTransaction(connId: string, tabId: string): Promise<string> {
  const res = await post('/api/rollback-tx', { connId, tabId, database: '' });
  return (res as { status: string }).status;
}

export async function TransactionStatus(tabId: string): Promise<string> {
  const res = await post('/api/tx-status', { connId: '', tabId, database: '' });
  return (res as { status: string }).status;
}

// ExecuteQueryInTab runs a statement inside the tab's transaction when it has one.
export async function ExecuteQueryInTab(
  connId: string,
  tabId: string,
  query: string,
  database = '',
): Promise<QueryResult> {
  return post('/api/execute-in-tab', { connId, tabId, query, database });
}

export async function ExecuteQueryInTabConfirmed(
  connId: string,
  tabId: string,
  query: string,
  database = '',
): Promise<QueryResult> {
  return post('/api/execute-in-tab-confirmed', { connId, tabId, query, database });
}

// CancelQuery asks the backend to abort every statement still running on the
// connection, which cancels its context and makes the driver abort server-side.
export async function CancelQuery(connId: string): Promise<{ cancelled: number }> {
  return post('/api/cancel-query', { connId });
}

export interface ImportOptions {
  format: 'csv' | 'json' | '';
  hasHeader: boolean;
  delimiter: string;
  createTable: boolean;
  inferTypes: boolean;
  skipErrors: boolean;
  mode: 'append' | 'truncate' | 'upsert';
  upsertKeys?: string[];
}

export interface ImportResult {
  rows: number;
  skipped: number;
  statements: number;
  columns: string[];
  createdTable: boolean;
  error?: string;
}

// ImportFile parses a CSV/JSON file server-side and writes it into a table.
export async function ImportFile(req: {
  connId: string;
  database: string;
  table: string;
  path: string;
  options: ImportOptions;
}): Promise<ImportResult> {
  return post('/api/import', req);
}

// One NDJSON line of /api/execute-stream (stream.go).
export interface StreamEvent {
  type: 'columns' | 'rows' | 'done' | 'error';
  columns?: Column[];
  rows?: any[][];
  rowCount?: number;
  truncated?: boolean;
  executionTime?: number;
  error?: string;
  needsConfirmation?: boolean;
}

export interface StreamHandlers {
  onColumns?: (columns: Column[]) => void;
  onRows?: (rows: any[][]) => void;
}

// ExecuteQueryStream reads the NDJSON response and calls the handlers as rows
// arrive, resolving with the final "done"/"error" event. A Safe Mode pause comes
// back as type "error" with needsConfirmation set (nothing ran).
export async function ExecuteQueryStream(
  connId: string,
  query: string,
  database = '',
  handlers: StreamHandlers = {},
): Promise<StreamEvent> {
  const res = await fetch(`${BASE}/api/execute-stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-API-Token': API_TOKEN },
    body: JSON.stringify({ connId, query, database }),
  });
  if (!res.ok || !res.body) {
    throw new Error(`stream failed: HTTP ${res.status}`);
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let final: StreamEvent | null = null;
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let newline = buffer.indexOf('\n');
    while (newline >= 0) {
      const line = buffer.slice(0, newline).trim();
      buffer = buffer.slice(newline + 1);
      if (line) {
        const event = JSON.parse(line) as StreamEvent;
        if (event.type === 'columns') handlers.onColumns?.(event.columns ?? []);
        else if (event.type === 'rows') handlers.onRows?.(event.rows ?? []);
        else final = event;
      }
      newline = buffer.indexOf('\n');
    }
  }
  if (!final) throw new Error('stream ended without a terminal event');
  return final;
}

// Columns of whichever table the query reads from; used for result comments
// and editor completion without parsing SQL on this side.
export async function QueryColumns(connId: string, query: string, database = ''): Promise<ColumnInfo[]> {
  return post('/api/query-columns', { connId, query, database });
}

// The table a statement reads rows from, or null when the result is not tied to
// one and therefore cannot be edited.
export async function GetQuerySource(
  connId: string,
  query: string,
  database = '',
): Promise<TableRef | null> {
  const ref = await post('/api/query-source', { connId, query, database });
  return ref && ref.table ? ref : null;
}

// Statements the backend would run for these edits, without running them.
export async function BuildChanges(set: ChangeSet): Promise<string[]> {
  const data = await post('/api/build-changes', set);
  return data.statements ?? [];
}

// Applies the same edits inside one transaction; the SQL is rebuilt server-side,
// so what was previewed is what runs.
export async function ApplyChanges(set: ChangeSet): Promise<number> {
  const data = await post('/api/apply-changes', set);
  return data.appliedRows;
}

// Starter DDL for "新建表"; the dialect lives in the shared SQL text package
// so the webview and the Fyne client open on one template.
export async function GetCreateTableTemplate(dbType: string, schema: string): Promise<string> {
  const data = await post('/api/create-table-template', { dbType, schema });
  return data.template;
}

export type Theme = 'system' | 'light' | 'dark';

// SafeMode mirrors the backend levels (safemode.go): off, confirm every write,
// confirm UPDATE/DELETE without WHERE, or block them outright.
export type SafeMode = 'off' | 'confirmWrites' | 'confirmNoWhere' | 'blockNoWhere';

export interface AppSettings {
  showColumnComments: boolean;
  // Rows a SELECT is capped at; 0 means no limit.
  resultLimit: number;
  theme: Theme;
  safeMode: SafeMode;
  // MCP write gate and per-query row cap; mirror of the backend AppSettings.
  mcpAllowWrites: boolean;
  mcpMaxRows: number;
  // Cap for one INSERT-script export (XLSX gets ten times); 0 uses the default.
  exportMaxRows: number;
}

export async function GetSettings(): Promise<AppSettings> {
  return get('/api/settings');
}

export async function SaveSettings(settings: AppSettings): Promise<void> {
  await post('/api/settings', settings);
}
