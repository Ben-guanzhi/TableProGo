import { ChangeSet, ForeignKeyInfo, HistoryEntry, SqlFilter } from './types';

// Injected by the Go asset handler; the ?apiPort= fallback keeps the vite dev
// server usable from a plain browser.
const API_PORT = (window as any).__API_PORT__
  ?? new URLSearchParams(window.location.search).get('apiPort');
const BASE = `http://127.0.0.1:${API_PORT}`;

async function post(path: string, body: any = {}): Promise<any> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  // Endpoints answer `null` for an empty catalog (a sqlite connection has no
  // schemas), which is a result, not an error.
  if (data?.error) throw new Error(data.error);
  return data;
}

async function get(path: string): Promise<any> {
  const res = await fetch(`${BASE}${path}`);
  const data = await res.json();
  if (data?.error) throw new Error(data.error);
  return data;
}

export async function GetConnections(): Promise<any[]> {
  return get('/api/connections');
}

export async function AddConnection(config: any): Promise<void> {
  await post('/api/connections', config);
}

export async function UpdateConnection(config: any): Promise<void> {
  await post('/api/connections/update', config);
}

export async function DeleteConnection(id: string): Promise<void> {
  await post('/api/connections/delete', { id });
}

export async function TestConnection(config: any): Promise<void> {
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

export async function GetTables(connId: string, database: string, schema = ''): Promise<any[]> {
  return post('/api/tables', { connId, database, schema });
}

export async function GetSchemas(connId: string, database: string): Promise<string[]> {
  return post('/api/schemas', { connId, database });
}

export async function GetColumns(connId: string, database: string, schema: string, table: string): Promise<any[]> {
  return post('/api/columns', { connId, database, schema, table });
}

export async function PreviewTable(
  ref: TableRef,
  limit = 100,
  offset = 0,
  filters: SqlFilter[] = [],
): Promise<any> {
  return post('/api/preview', { ...ref, limit, offset, filters });
}

export type TableDdlAction = 'drop' | 'truncate' | 'rename';

// Rename/truncate/drop one object. The backend renders the statement from the
// names in its own catalog - and refuses anything that is not there - so no
// text typed in the menu can reach the database as SQL.
export async function RunTableAction(
  ref: TableRef,
  action: TableDdlAction,
  newName = '',
): Promise<string> {
  const data = await post('/api/table-action', { ...ref, action, newName });
  return data.statement ?? '';
}

export async function GetTableStructure(connId: string, database: string, schema: string, table: string): Promise<any> {
  return post('/api/structure', { connId, database, schema, table });
}

export interface TableRef {
  connId: string;
  database: string;
  schema: string;
  table: string;
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

export async function ExecuteQuery(connId: string, query: string, database = ''): Promise<any> {
  return post('/api/execute', { connId, query, database });
}

// Columns of whichever table the query reads from; used for result comments
// and editor completion without parsing SQL on this side.
export async function QueryColumns(connId: string, query: string, database = ''): Promise<any[]> {
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

export type Theme = 'system' | 'light' | 'dark';export interface AppSettings {
  showColumnComments: boolean;
  // Rows a SELECT is capped at; 0 means no limit.
  resultLimit: number;
  theme: Theme;
}

export async function GetSettings(): Promise<AppSettings> {
  return get('/api/settings');
}

export async function SaveSettings(settings: AppSettings): Promise<void> {
  await post('/api/settings', settings);
}
