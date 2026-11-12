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
  ERFK,
  EnumTypeInfo,
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
// W16: two deployment shapes share this client.
//
//   desktop  the Wails host injects __API_PORT__/__API_TOKEN__; the API is a different
//            origin (127.0.0.1:port) and the token header is the credential.
//   server   the page IS served by the API, so no port is injected: requests go to the
//            page's own origin, the browser attaches the HttpOnly session cookie by
//            itself, and there is no token to send.
//
// Getting this wrong is not cosmetic: sending the desktop token from a server page
// would 401 (server mode ignores it), and hard-coding 127.0.0.1 in server mode would
// point every request at the viewer's own machine.
export const SAME_ORIGIN = !API_PORT;
const BASE = SAME_ORIGIN ? '' : `http://127.0.0.1:${API_PORT}`;

// authHeaders are what the credential looks like in the current shape. In server mode
// it is empty on purpose: the cookie is attached by the browser, and a token here
// would be a second, weaker credential.
const authHeaders = (): Record<string, string> =>
  API_TOKEN ? { 'X-API-Token': API_TOKEN } : {};

// AuthRequiredError marks "you are not logged in", which the UI turns into a login
// screen rather than an error toast.
export class AuthRequiredError extends Error {
  constructor() {
    super('需要登录');
    this.name = 'AuthRequiredError';
  }
}

export interface RequestOptions {
  signal?: AbortSignal;
}

// parseResponse turns the API's conventions into results and errors: the
// `error` field is a failed call, and a non-JSON body (a proxy error page, a
// crashed process) is reported as an HTTP failure instead of surfacing as a
// JSON parse accident.
async function parseResponse(res: Response): Promise<any> {
  // 401 in server mode means "log in", not "the call failed": the UI redirects to the
  // login screen instead of showing an error the user cannot act on.
  if (res.status === 401 && SAME_ORIGIN) throw new AuthRequiredError();
  let data: any;
  try {
    data = await res.json();
  } catch {
    throw new Error(`HTTP ${res.status} ${res.statusText || '请求失败'}`);
  }
  if (data?.error) throw new Error(data.error);
  if (!res.ok) throw new Error(`HTTP ${res.status} ${res.statusText || '请求失败'}`);
  return data;
}

export async function post(path: string, body: any = {}, opts: RequestOptions = {}): Promise<any> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    // Same-origin requests carry the session cookie; explicit here so a future
    // cross-origin deployment cannot silently drop it.
    credentials: SAME_ORIGIN ? 'same-origin' : 'omit',
    body: JSON.stringify(body),
    signal: opts.signal,
  });
  return parseResponse(res);
}

async function get(path: string): Promise<any> {
  const res = await fetch(`${BASE}${path}`, {
    headers: { ...authHeaders() },
    credentials: SAME_ORIGIN ? 'same-origin' : 'omit',
  });
  return parseResponse(res);
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

// Local passcode (P2-9). The frontend never stores a hash or the passcode
// itself — verification is a backend call (passcode.go), so the lock lives
// with the argon2id store, not in localStorage.
export async function HasPasscode(): Promise<boolean> {
  const data = await post('/api/passcode/has');
  return !!data.has;
}

export async function SetPasscode(passcode: string): Promise<void> {
  await post('/api/passcode/set', { passcode });
}

export async function VerifyPasscode(passcode: string): Promise<boolean> {
  const data = await post('/api/passcode/verify', { passcode });
  return !!data.ok;
}

export async function ClearPasscode(): Promise<void> {
  await post('/api/passcode/clear');
}

// OS-reported idle seconds, or -1 when the platform does not report it; the
// watcher then falls back to in-page activity.
export async function SystemIdleSeconds(): Promise<number> {
  const data = await post('/api/system-idle');
  return data.seconds;
}

// The lock disconnects everything behind it so no live session outlives the
// lock screen.
export async function DisconnectAll(): Promise<void> {
  await post('/api/disconnect-all');
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
  // Folder groups favorites in the menu (P2-3); empty = ungrouped.
  folder?: string;
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
  folder = '',
): Promise<Favorite> {
  return post('/api/favorites', { connId, database, name, sql, folder });
}

export async function DeleteFavorite(id: string): Promise<void> {
  await post('/api/favorites/delete', { id });
}

// W1-b: rename a folder across every entry it holds, or dissolve it (entries
// move back to the ungrouped list). Both answer with the moved count.
export async function RenameFavoriteFolder(from: string, to: string): Promise<number> {
  const data = await post('/api/favorites/folder/rename', { from, to });
  return data.moved ?? 0;
}

export async function UnfileFavoriteFolder(folder: string): Promise<number> {
  const data = await post('/api/favorites/folder/unfile', { folder });
  return data.moved ?? 0;
}

// ---- W4: connection URL + ~/.ssh/config ----

export interface SSHRejectedDirective {
  Line: number;
  Directive: string;
  Reason: string;
}

export interface SSHResolveResult {
  found: boolean;
  alias: string;
  hostName: string;
  user: string;
  port: number;
  keyFiles: string[] | null;
  rejected: SSHRejectedDirective[] | null;
  warnings: string[] | null;
}

export interface SSHConfigHost {
  alias: string;
  hostName: string;
  user: string;
  port: number;
}

export interface SSHConfigSummary {
  hosts: SSHConfigHost[] | null;
  rejected: SSHRejectedDirective[] | null;
  warnings: string[] | null;
}

// What the user's own ssh config says about a host. A missing config answers
// found=false rather than failing: "nothing written down" is a valid answer.
export async function ResolveSSHConfig(host: string): Promise<SSHResolveResult> {
  return post('/api/ssh-config/resolve', { host });
}

export async function LoadSSHConfig(): Promise<SSHConfigSummary> {
  return post('/api/ssh-config/load', {});
}

// A pasted connection URL, parsed into the dialog's own ConnectionConfig shape.
// The backend owns the scheme table, so an unsupported scheme is refused with
// its name instead of connecting somewhere else.
export async function ParseConnectionURI(uri: string): Promise<{
  config: Partial<ConnectionConfig>;
  masked: string;
}> {
  return post('/api/parse-uri', { uri });
}

// ---- W5: server-to-server table copy ----

export interface TransferTarget {
  connId: string;
  database: string;
  schema: string;
}

export interface TransferRequest {
  source: TransferTarget;
  target: TransferTarget;
  // Empty copies every base table of the source schema.
  tables: string[];
  ignoreForeignKeys?: boolean;
  skipData?: boolean;
  // Required on apply: the backend refuses a copy nobody confirmed.
  confirm?: boolean;
}

export interface TransferTableInfo {
  source: string;
  target: string;
  // -1 when the source count could not be read.
  rows: number;
  deferred: boolean;
}

export interface TransferStatement {
  table: string;
  sql: string;
  kind: string;
}

export interface TransferTypeLoss {
  Table: string;
  Column: string;
  Source: string;
  Target: string;
  Reason: string;
}

// The plan a transfer WOULD run: nothing is written until Apply. Reading it is
// what lets the user see the order, the row counts and the DDL first.
export interface TransferPreview {
  tables: TransferTableInfo[] | null;
  statements: TransferStatement[] | null;
  cycles: { Tables: string[] }[] | null;
  warnings: string[] | null;
  losses: TransferTypeLoss[] | null;
  totalRows: number;
}

export interface TransferTableResult {
  source: string;
  target: string;
  rowsRead: number;
  rowsWritten: number;
  error?: string;
}

export interface TransferResult {
  tables: TransferTableResult[] | null;
  rows: number;
  aborted: boolean;
  error?: string;
  elapsed: string;
}

export async function PreviewTransfer(req: TransferRequest): Promise<TransferPreview> {
  return post('/api/transfer/preview', req);
}

// Apply re-derives the plan server-side and requires confirm, so a stale or
// forged preview cannot drive it.
export async function ApplyTransfer(req: TransferRequest): Promise<TransferResult> {
  return post('/api/transfer/apply', { ...req, confirm: true });
}

// ---- W6: row-level sync ----

export interface RowSyncRequest {
  source: TransferTarget;
  target: TransferTarget;
  table: string;
  // Columns that never count as a difference (touched-at, auto-increment).
  ignoreColumns?: string[];
}

export interface RowSyncChange {
  seq: number;
  op: 'insert' | 'update' | 'delete';
  // The primary-key tuple, rendered for display.
  key: string;
  values?: Record<string, unknown> | null;
}

// The plan summary: counts plus a bounded sample. The full plan stays on the
// server, addressed by planId.
export interface RowSyncPreview {
  planId: string;
  table: string;
  pk: string[] | null;
  inserts: number;
  updates: number;
  deletes: number;
  total: number;
  sample: RowSyncChange[] | null;
  truncated: boolean;
  sourceRows: number;
  targetRows: number;
  elapsed: string;
}

export interface RowSyncApplyRequest {
  planId: string;
  // The changes to run, by seq. Empty applies nothing.
  seqs: number[];
  confirm: boolean;
}

export interface RowSyncApplyResult {
  applied: number;
  total: number;
  inserts: number;
  updates: number;
  deletes: number;
  error?: string;
  elapsed: string;
}

// Preview merges both tables and stores the plan. Read-only: nothing is written
// until apply.
export async function PreviewRowSync(req: RowSyncRequest): Promise<RowSyncPreview> {
  return post('/api/rowsync/preview', req);
}

// Apply runs the ticked changes in one transaction; the server re-checks that
// the plan still exists and that confirm arrived.
export async function ApplyRowSync(req: RowSyncApplyRequest): Promise<RowSyncApplyResult> {
  return post('/api/rowsync/apply', { ...req, confirm: true });
}

// ---- W10: plugins panel ----

export type PluginState = 'trusted' | 'unknown' | 'tampered';

export interface PluginInfo {
  type: string;
  name: string;
  path: string;
  version?: string;
  description?: string;
  // "registry" (declared in plugin_registry.json) or "env" (TABLEPRO_PLUGIN_<TYPE>).
  source: string;
  state: PluginState;
  detail: string;
  expected?: string;
  actual?: string;
  enabled: boolean;
  exists: boolean;
}

export interface PluginPanelData {
  plugins: PluginInfo[] | null;
  registryPath: string;
  trustPath: string;
  warnings: string[] | null;
}

// The panel reports the same three states the startup gate acts on, so it can
// never show "trusted" for a file the gate would refuse.
export async function ListPlugins(): Promise<PluginPanelData> {
  return get('/api/plugins');
}

// Trusting records the CURRENT digest, which is what turns "unknown" (or a
// re-built plugin) into "trusted" on the next start.
export async function TrustPlugin(path: string, type: string): Promise<PluginInfo> {
  return post('/api/plugins/trust', { path, type });
}

export async function UntrustPlugin(path: string): Promise<void> {
  await post('/api/plugins/untrust', { path });
}

// Writes the starter registry on first open, so the panel points at a file the
// user can edit instead of showing a blank list with no format hint.
export async function EnsurePluginRegistry(): Promise<{ path: string }> {
  return post('/api/plugins/ensure-registry', {});
}

// ---- W11: visual execution plan ----

export interface PlanNode {
  label: string;
  detail?: string;
  cost?: number;
  // -1 when the engine reported no row estimate.
  rows: number;
  children?: PlanNode[];
  depth: number;
  // A one-line hint the renderer highlights (a full scan, a filesort).
  warn?: string;
}

export interface ExplainPlanData {
  // Absent when the engine only offers prose (Oracle), in which case the caller
  // shows `raw` instead of drawing.
  root?: PlanNode;
  format: string;
  raw: string;
  note?: string;
  nodes: number;
}

// The same statement, asked for in the engine's machine-readable plan format.
// Nothing in a plan executes the statement, so there is no Safe Mode gate.
export async function ExplainPlanTree(
  connId: string,
  query: string,
  database = '',
): Promise<ExplainPlanData> {
  return post('/api/explain-plan', { connId, query, database });
}

// ---- W12: importing connections from another client ----

export interface ConnImportRequest {
  path: string;
  // Empty detects from the file's shape; "dbeaver" | "url" | "csv".
  format?: string;
  apply?: boolean;
  group?: string;
}

export interface ConnImportRow {
  name: string;
  engine: string;
  host: string;
  port: number;
  database: string;
  user: string;
  ssl?: string;
  source: string;
}

export interface ConnImportSkip {
  where: string;
  reason: string;
}

export interface ConnImportResult {
  rows: ConnImportRow[] | null;
  skipped: ConnImportSkip[] | null;
  notes: string[] | null;
  imported: number;
  renamed: string[] | null;
}

// Read a file and (with apply) save what it understood. A preview is the
// default: the same preview-then-apply discipline as transfer and sync.
export async function ImportConnections(req: ConnImportRequest): Promise<ConnImportResult> {
  return post('/api/connections/import', req);
}

// W13: write the agent skill file (~/.agents/skills/tablepro-go/SKILL.md) so a
// shell agent discovers how to drive the job/MCP surfaces. Overwritten on
// purpose: a stale copy would describe flags that changed.
export async function WriteAgentSkill(): Promise<{ path: string }> {
  return post('/api/agent-skill', {});
}

// ---- W9: Oracle object health ----

export interface OracleObject {
  name: string;
  type: string;
}

export interface OracleObjectHealth {
  // false for a non-Oracle connection: the panel hides rather than showing an
  // empty list that looks like "everything is fine".
  supported: boolean;
  objects: OracleObject[] | null;
  note?: string;
}

export interface OracleCompileError {
  line: number;
  position: number;
  text: string;
}

export interface OracleRecompileResult {
  // Whether the ALTER ... COMPILE statement itself ran.
  compiled: boolean;
  // The real answer: Oracle accepts the statement even when the object cannot be
  // compiled, so this must be read from the catalog afterwards.
  stillInvalid: boolean;
  errors: OracleCompileError[] | null;
  error?: string;
}

export interface OracleDependency {
  direction: 'in' | 'out';
  name: string;
  type: string;
  owner: string;
}

export async function ListInvalidObjects(
  connId: string,
  database = '',
  schema = '',
): Promise<OracleObjectHealth> {
  return post('/api/oracle/invalid-objects', { connId, database, schema });
}

export async function RecompileObject(req: {
  connId: string;
  database?: string;
  schema: string;
  name: string;
  type: string;
}): Promise<OracleRecompileResult> {
  return post('/api/oracle/recompile', { ...req, confirm: true });
}

export async function ListObjectDependencies(
  connId: string,
  schema: string,
  object: string,
  database = '',
): Promise<{ dependencies: OracleDependency[] | null }> {
  return post('/api/oracle/dependencies', { connId, database, schema, object });
}

// ---- W15: build identity and the opt-in update check ----

export interface BuildInfo {
  version: string;
  commit: string;
  date: string;
  go: string;
  os: string;
  arch: string;
}

export interface UpdateCheckResult {
  current: string;
  // Present only when a NEWER version was published.
  newer?: string;
  notes?: string;
  // false when no URL is configured: the UI must not claim it checked.
  checked: boolean;
  error?: string;
}

export async function GetBuildInfo(): Promise<BuildInfo> {
  return post('/api/build-info', {});
}

// Runs only when the user has configured a URL; the app never phones home on its
// own, and the request is a bare GET with no body.
export async function CheckUpdate(url: string): Promise<UpdateCheckResult> {
  return post('/api/update-check', { url });
}

// ---- W11b: SQL notebooks ----

export interface NotebookCell {
  // Stable across reordering, so a cell keeps its identity when moved.
  id: string;
  // The cell's own text, {{cell_N}} references included.
  sql: string;
  title?: string;
}

export interface Notebook {
  id: string;
  name: string;
  connection: string;
  database?: string;
  cells: NotebookCell[];
  updatedAt?: string;
}

export interface NotebookRunResult {
  result: QueryResult;
  // The EXPANDED statement that actually ran — shown on demand, because it is the
  // answer to "why did this cell behave differently from what it looks like".
  statement: string;
  // The 1-based cells that were inlined.
  dependsOn?: number[] | null;
  // A notebook-level failure (bad reference, empty cell); a statement failure is in
  // result.error, like the editor's.
  error?: string;
}

export async function LoadNotebooks(): Promise<{ notebooks: Notebook[] | null }> {
  return post('/api/notebooks', {});
}

export async function SaveNotebook(nb: Notebook): Promise<{ notebooks: Notebook[] | null }> {
  return post('/api/notebooks/save', nb);
}

export async function DeleteNotebook(id: string): Promise<{ notebooks: Notebook[] | null }> {
  return post('/api/notebooks/delete', { id });
}

// The server expands references and runs the statement; the editor keeps the cell's
// own text so {{cell_1}} stays visible and editable.
export async function RunNotebookCell(nb: Notebook, index: number): Promise<NotebookRunResult> {
  return post('/api/notebooks/run', { notebook: nb, index });
}

// Pretty-print SQL: keywords upper-cased, one clause per line. The rules live
// in the Go formatter (internal/sqlformat), so every client shares one style.
// preset selects the layout (W2-c): default / compact / expanded.
export async function FormatSQL(sql: string, preset = ''): Promise<string> {
  const data = await post('/api/format-sql', { sql, preset });
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

// One continuation window of a key's value (P3-3): the cursor is the `next`
// the previous window returned. The panel appends its rows to what it shows.
export async function GetKVValuePage(
  connId: string,
  database: string,
  key: string,
  cursor: string,
): Promise<KVValue> {
  return post('/api/kv-value-page', { connId, database, key, cursor });
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
  tabId = '',
): Promise<any> {
  return post('/api/preview', {
    ...ref,
    tabId,
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
// the statement, one confirmation is collected through `confirm` — the app
// passes its themed ConfirmDialog; cancelling throws, so the caller treats it
// like any other aborted action. The confirm may be asynchronous and is always
// awaited, so a promise-based dialog cannot be mistaken for an approval.
// ExplainQuery renders one statement's execution plan as a result grid.
// Nothing in a plan executes the statement (EXPLAIN DELETE does not delete),
// so Safe Mode has no say here; the result carries no write-back source.
export async function ExplainQuery(
  connId: string,
  query: string,
  database: string,
): Promise<QueryResult> {
  return (await post('/api/explain', { connId, query, database })) as unknown as QueryResult;
}

export async function RunTableAction(
  ref: TableRef,
  action: TableDdlAction,
  newName: string,
  confirm: (message: string) => boolean | Promise<boolean>,
): Promise<string> {
  const data = await post('/api/table-action', { ...ref, action, newName });
  if (data.needsConfirmation) {
    const reason: string = data.reason ?? '';
    if (!(await confirm(`${reason}\n\n仍要执行这条语句吗？`))) {
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

// The schema's user-defined enum types (PostgreSQL only). Other engines answer
// an error naming the reason instead of an empty list.
export async function GetEnumTypes(
  connId: string,
  database: string,
  schema: string,
): Promise<EnumTypeInfo[]> {
  return post('/api/enums', { connId, database, schema });
}

export interface ColumnEdit {
  name: string;
  type: string;
  nullable: boolean;
  defaultValue: string;
  isNew?: boolean;
}

// P2-7: the index / foreign-key tabs round-trip the catalog the same way the
// column list does; the backend diffs and renders CREATE/DROP through ddl.
export interface IndexEdit {
  name: string;
  unique: boolean;
  columns: string[];
  isNew?: boolean;
}

export interface ForeignKeyEdit {
  name: string;
  columns: string[];
  refSchema: string;
  refTable: string;
  refColumns: string[];
  onDelete: string;
  onUpdate: string;
  isNew?: boolean;
}

export interface StructureEditResult {
  statements: string[];
  warnings: string[];
  // Applied echoes the statement list a rollback request says it executed, so the
  // response stays traceable back to the edit it undoes.
  applied?: string[];
}

// The before-snapshot a structure-edit rollback renders from: the catalog as it
// stood before the edit was applied, in the same shapes the editor edits.
export interface StructureEditSnapshot {
  columns: ColumnEdit[];
  indexes?: IndexEdit[];
  foreignKeys?: ForeignKeyEdit[];
}

// A rollback request is the applied edit plus the before-snapshot; the backend
// renders the reverse through the kernel ddl package and gates it under Safe Mode.
export interface StructureRollbackRequest {
  connId: string;
  database: string;
  schema: string;
  table: string;
  columns: ColumnEdit[];
  indexes?: IndexEdit[];
  foreignKeys?: ForeignKeyEdit[];
  before: StructureEditSnapshot;
  statements?: string[];
}

export async function ApplyStructureRollback(req: StructureRollbackRequest): Promise<StructureEditResult> {
  return post('/api/structure-edit/rollback', req);
}

export async function ApplyStructureRollbackConfirmed(req: StructureRollbackRequest): Promise<StructureEditResult> {
  return post('/api/structure-edit/rollback-confirmed', req);
}

// The ERD foreign-key editor's payload: one add/drop/rollback on one table. The
// `preview` flag mirrors the structure editor — preview only renders, apply runs
// under the same Safe Mode pause.
export type ERFKEditAction = 'add' | 'drop' | 'rollback';

export interface ERFKEditRequest {
  connId: string;
  database: string;
  schema: string;
  table: string;
  action: ERFKEditAction;
  preview: boolean;
  // add (and a rollback of a drop): the key definition.
  name?: string;
  columns?: string[];
  refSchema?: string;
  refTable?: string;
  refColumns?: string[];
  onDelete?: string;
  onUpdate?: string;
  // drop (and a rollback of an add): the key name.
  fkName?: string;
  // rollback: which applied action this undoes; the backend infers it from the
  // catalog when omitted.
  rollbackOf?: string;
}

export interface ERFKEditResult {
  statements: string[];
  warnings: string[];
  // The statements that undo this edit; the UI offers "撤销" after applying.
  rollback: string[];
}

export async function PreviewERFKEdit(req: ERFKEditRequest): Promise<ERFKEditResult> {
  return post('/api/er/fk', { ...req, preview: true });
}

export async function ApplyERFKEdit(req: ERFKEditRequest): Promise<ERFKEditResult> {
  return post('/api/er/fk', { ...req, preview: false });
}

// The Safe Mode pause comes back structured; the UI confirms and re-sends here.
export async function ApplyERFKEditConfirmed(req: ERFKEditRequest): Promise<ERFKEditResult> {
  return post('/api/er/fk-confirmed', { ...req, preview: false });
}

export async function PreviewStructureEdit(req: {
  connId: string;
  database: string;
  schema: string;
  table: string;
  columns: ColumnEdit[];
  indexes?: IndexEdit[];
  foreignKeys?: ForeignKeyEdit[];
}): Promise<StructureEditResult> {
  return post('/api/structure-preview', req);
}

export async function ApplyStructureEdit(req: {
  connId: string;
  database: string;
  schema: string;
  table: string;
  columns: ColumnEdit[];
  indexes?: IndexEdit[];
  foreignKeys?: ForeignKeyEdit[];
}): Promise<StructureEditResult> {
  return post('/api/structure-edit', req);
}

// The Safe Mode pause comes back structured (needsConfirmation + reason);
// the dialog confirms and re-sends through this route.
export async function ApplyStructureEditConfirmed(req: {
  connId: string;
  database: string;
  schema: string;
  table: string;
  columns: ColumnEdit[];
  indexes?: IndexEdit[];
  foreignKeys?: ForeignKeyEdit[];
}): Promise<StructureEditResult> {
  return post('/api/structure-edit-confirmed', req);
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
// from, so the cells can be numbers and dates rather than text. The response is
// the file itself (P1-13): base64-in-JSON made both sides hold a second
// full-size copy of every export.
const XLSX_MIME = 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet';

export async function ExportWorkbook(
  columns: { name: string; type: string }[],
  rows: any[][],
): Promise<Blob> {
  const res = await fetch(`${BASE}/api/export-xlsx`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-API-Token': API_TOKEN },
    body: JSON.stringify({ columns, rows }),
  });
  if (!res.ok) {
    // A refusal (row cap, empty result) is still JSON; surface its message
    // instead of the bare status.
    let message = `导出失败：HTTP ${res.status}`;
    try {
      const data = await res.json();
      if (data?.error) message = data.error;
    } catch {
      /* not JSON: keep the status text */
    }
    throw new Error(message);
  }
  return new Blob([await res.blob()], { type: XLSX_MIME });
}

// One catalog hit from the name search behind the quick-open box.
export interface ObjectMatch extends TableRef {
  // "key" is a key/value connection's object: picking it opens the value tab.
  type: 'table' | 'view' | 'key';
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
//
// BeginTransaction answers with the status AND the structured detail in one
// round trip: the frontend used to issue Begin then TransactionStatusDetail as
// two awaits in one try block, and a failed second left the transaction open
// server-side with no badge (the deadline calibrator only re-syncs tabs that
// already carry a badge, so it stayed invisible until the sweeper rolled it
// back).
export interface BeginTxResponse {
  status: string;
  detail: {
    status: string;
    remainingMs: number;
    warned: boolean;
    note?: string;
  };
}

export async function BeginTransaction(connId: string, tabId: string, database = ''): Promise<BeginTxResponse> {
  const res = await post('/api/begin-tx', { connId, tabId, database });
  const r = res as BeginTxResponse;
  // Tolerate an older backend that still answers {status} only.
  if (r && typeof r === 'object' && 'detail' in r) return r;
  const status = (res as { status: string }).status;
  return { status, detail: { status, remainingMs: 0, warned: false } };
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

// TxStatusDetail is /api/tx-status's structured answer: the one-line status
// plus the deadline fields (backend txn.go TxStatusDetail) the editor turns
// into a countdown. remainingMs is 0 when the session has no deadline
// (governance off or no transaction open); note is non-empty only after an
// automatic rollback.
export interface TxStatusDetail {
  status: string;
  remainingMs: number;
  warned: boolean;
  note?: string;
}

export async function TransactionStatusDetail(tabId: string): Promise<TxStatusDetail> {
  const res = await post('/api/tx-status', { connId: '', tabId, database: '' });
  return res as TxStatusDetail;
}

// ForgetTxSession rolls back and drops a tab's transaction state on tab
// close — without it the server-side transaction hung until disconnect.
export async function ForgetTxSession(tabId: string): Promise<void> {
  await post('/api/tx-forget', { connId: '', tabId, database: '' });
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

// CancelQuery aborts the statements still running for the connection. A tabId
// narrows it to that tab's statement — one tab's cancel must not kill a
// neighbouring tab's long query. Empty tabId keeps the connection-wide sweep.
export async function CancelQuery(connId: string, tabId = ''): Promise<{ cancelled: number }> {
  return post('/api/cancel-query', { connId, tabId });
}

export interface ImportOptions {
  format: 'csv' | 'json' | 'xlsx' | 'xlsm' | 'sql' | '';
  hasHeader: boolean;
  delimiter: string;
  createTable: boolean;
  inferTypes: boolean;
  skipErrors: boolean;
  mode: 'append' | 'truncate' | 'upsert';
  upsertKeys?: string[];
}

/** One work unit of a multi-file / multi-sheet import. */
export interface ImportTableResult {
  path?: string;
  table: string;
  sheet?: string;
  rows: number;
  skipped: number;
  statements: number;
  columns?: string[];
  createdTable: boolean;
  mapped?: { file: string; table?: string; suggested?: string }[];
  firstError?: string;
  error?: string;
}

export interface ImportResult {
  rows: number;
  skipped: number;
  statements: number;
  columns: string[];
  createdTable: boolean;
  error?: string;
  /** File-column → table-column pairing for an existing-table import. */
  mapped?: { file: string; table?: string; suggested?: string }[];
  /** First skipped row's error, with its 1-based file row number. */
  firstError?: string;
  /** Per-table breakdown of a multi-file / multi-sheet import. */
  tables?: ImportTableResult[];
}

// ImportRequest is the payload of /api/import and /api/import/preview. Only the
// fields a single-file, single-table import needs are required; the rest are
// additive.
export interface ImportRequest {
  connId: string;
  database: string;
  schema: string;
  /** Target table. Empty with an xlsx workbook means one table per worksheet. */
  table: string;
  path: string;
  paths?: string[];
  sheet?: string;
  sheets?: { sheet: string; table: string }[];
  /** Authoritative file-column → table-column mapping. An empty map falls back
   * to auto-matching; a non-empty one skips every source it does not name. */
  columnMap?: Record<string, string>;
  skipColumns?: string[];
  options: ImportOptions;
}

// ImportFile parses a CSV/JSON/XLSX file server-side and writes it into a table.
export async function ImportFile(req: ImportRequest): Promise<ImportResult> {
  return post('/api/import', req);
}

/** One work unit of a preview. */
export interface ImportTablePreview {
  path?: string;
  sheet?: string;
  table: string;
  exists: boolean;
  /** The file's columns, in file order. */
  columns: string[];
  /** The target table's own columns (when it exists), for the mapping editor. */
  targetColumns?: string[];
  mapped?: { file: string; table?: string; suggested?: string }[];
  ddl?: string;
  sample?: string[];
  rows: number;
  notes?: string[];
}

export interface ImportPreview {
  format?: string;
  paths?: string[];
  tables: ImportTablePreview[];
  notes?: string[];
  sql?: string[];
}

// PreviewImport resolves the request and renders the SQL it would run, without
// writing anything.
export async function PreviewImport(req: ImportRequest): Promise<ImportPreview> {
  return post('/api/import/preview', req);
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
  // AbortSignal for the stream itself: aborting closes the fetch and ends the
  // reader loop immediately, instead of waiting for the server to finish
  // pushing after the UI-side cancel (the backend cancel-query stops the
  // statement, but the browser-side fetch used to keep draining until then).
  signal?: AbortSignal;
}

// ExecuteQueryStream reads the NDJSON response and calls the handlers as rows
// arrive, resolving with the final "done"/"error" event. A Safe Mode pause comes
// back as type "error" with needsConfirmation set (nothing ran). An aborted
// signal rejects with the AbortError, so callers can tell a cancel from a
// server error.
export async function ExecuteQueryStream(
  connId: string,
  query: string,
  database = '',
  handlers: StreamHandlers = {},
  tabId = '',
): Promise<StreamEvent> {
  const res = await fetch(`${BASE}/api/execute-stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-API-Token': API_TOKEN },
    body: JSON.stringify({ connId, query, database, tabId }),
    signal: handlers.signal,
  });
  if (!res.ok || !res.body) {
    throw new Error(`stream failed: HTTP ${res.status}`);
  }
  const reader = res.body.getReader();
  const abort = () => reader.cancel().catch(() => {});
  if (handlers.signal) {
    // A cancel that lands mid-read unblocks the pending read() and ends the
    // loop; the fetch's own abort handling covers the pre-response window.
    handlers.signal.addEventListener('abort', abort, { once: true });
  }
  try {
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
  } finally {
    if (handlers.signal) {
      handlers.signal.removeEventListener('abort', abort);
    }
  }
}

// Columns of whichever table the query reads from; used for result comments
// and editor completion without parsing SQL on this side.
export async function QueryColumns(connId: string, query: string, database = ''): Promise<ColumnInfo[]> {
  return post('/api/query-columns', { connId, query, database });
}

// One NDJSON line of /api/search-data (whole-database data search, P2-10).
export interface SearchDataEvent {
  type: 'start' | 'table' | 'done';
  tables?: number;
  scanned?: number;
  total?: number;
  schema?: string;
  table?: string;
  error?: string;
  columns?: Column[];
  rows?: any[][];
  hits?: number;
  truncated?: boolean;
  cancelled?: boolean;
  elapsedSeconds?: number;
}

// SearchDatabaseData walks the keyword through every table of one database,
// handing each object's progress to onTable as it arrives. The AbortSignal
// cancels the fetch mid-stream; the server cancels its queries the same way
// (the request context is the parent of the run), so a stopped search never
// leaves a scan running on the database.
export async function SearchDatabaseData(
  connId: string,
  database: string,
  keyword: string,
  handlers: { onStart?: (tables: number) => void; onTable?: (event: SearchDataEvent) => void; signal?: AbortSignal } = {},
): Promise<SearchDataEvent> {
  const res = await fetch(`${BASE}/api/search-data`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-API-Token': API_TOKEN },
    body: JSON.stringify({ connId, database, keyword }),
    signal: handlers.signal,
  });
  if (!res.ok || !res.body) {
    throw new Error(`search failed: HTTP ${res.status}`);
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let final: SearchDataEvent | null = null;
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let newline = buffer.indexOf('\n');
    while (newline >= 0) {
      const line = buffer.slice(0, newline).trim();
      buffer = buffer.slice(newline + 1);
      if (line) {
        const event = JSON.parse(line) as SearchDataEvent;
        if (event.type === 'start') handlers.onStart?.(event.tables ?? 0);
        else if (event.type === 'table') handlers.onTable?.(event);
        else final = event;
      }
      newline = buffer.indexOf('\n');
    }
  }
  if (!final) throw new Error('搜索没有返回结束事件');
  return final;
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
  // Idle auto-lock (P2-9): 0 turns the idle rules off; the lock itself needs a
  // stored passcode, which the backend owns (passcode.go).
  idleLockMinutes: number;
  lockOnHide: boolean;
  lockOnSleep: boolean;
  // W8: blur connection identity and result cells for screen sharing. A DISPLAY
  // filter only — nothing stops being sent to the database, and the mask lifts on
  // hover, because it is meant to defeat a bystander or a capture, not the user.
  privacyMode: boolean;
  // Interactive-transaction governance (DESIGN-txn-timeout-2026-11-03): the
  // absolute deadline in minutes (0 = off, the unbounded behavior) and how
  // long before it the UI warns. Mirror of the backend AppSettings.
  txTimeoutMinutes: number;
  txWarnAheadMinutes: number;
  // W2-b: modal (Vim) keybindings in the SQL editor. Off by default.
  vimMode: boolean;
  // W2-c: the layout the format action applies — "default" | "compact" |
  // "expanded"; empty means default.
  formatPreset: string;
}

export async function GetSettings(): Promise<AppSettings> {
  return get('/api/settings');
}

export async function SaveSettings(settings: AppSettings): Promise<void> {
  await post('/api/settings', settings);
}
