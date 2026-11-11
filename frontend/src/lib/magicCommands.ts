// W1-a: the "魔法命令" port — statements that begin with `\` are intercepted
// before they reach the server and answered as result grids instead. The
// catalog questions ride the app's own HTTP API (tables/structure/routines),
// so the answers come from the same paths the sidebar reads and the dialect
// work stays in one place.
//
// The PARSER is pure and tested on its own (tests/magicCommands.test.ts);
// the EXECUTOR takes async fetchers so it can be tested with stubs, and App
// wires it to the real API. Nothing here ever sends a `\`-statement to the
// database — an unknown command is refused with the help text, not executed.

import type { ColumnInfo, QueryResult, TableStructure } from '../types/index';

export interface MagicFetchers {
  // The database's tables / views (name plus whatever the caller carries).
  listTables: () => Promise<{ name: string; type?: string }[]>;
  listViews: () => Promise<{ name: string; type?: string }[]>;
  listSchemas: () => Promise<string[]>;
  listRoutines: () => Promise<{ name: string; kind: string }[]>;
  describe: (table: string) => Promise<TableStructure>;
  // The table whose rows the tab currently shows, for \d / \di's optional
  // argument; empty when the tab shows no single table.
  sourceTable: () => string;
  // The whole-table export (\copy): returns the row count written, so the
  // toast can say what happened. The exporter owns dialogs and paths.
  copyTable: (table: string, format: CopyFormat, where: string) => Promise<number>;
}

export type CopyFormat = 'csv' | 'tsv' | 'json' | 'jsonl' | 'markdown' | 'excel';

const KNOWN_FORMATS: readonly CopyFormat[] = ['csv', 'tsv', 'json', 'jsonl', 'markdown', 'excel'];

const isKnownFormat = (s: string): s is CopyFormat =>
  (KNOWN_FORMATS as readonly string[]).includes(s.toLowerCase());

// parseMagicCommand splits `\cmd arg` into its command and argument. A
// statement that does not begin with a backslash is not ours — null, so the
// caller sends it to the database untouched. `\?` and `\help` carry no arg.
export const parseMagicCommand = (
  sql: string,
): { cmd: string; arg: string } | null => {
  const trimmed = sql.trim();
  if (!trimmed.startsWith('\\')) return null;
  const m = trimmed.match(/^\\([\w?]+)\s*([\s\S]*)$/);
  if (!m) return null;
  return { cmd: m[1].toLowerCase(), arg: (m[2] ?? '').trim() };
};

export const isMagicCommand = (sql: string): boolean => parseMagicCommand(sql) !== null;

// magicGrid assembles a QueryResult from a magic answer. Rows arrive as
// text already; NULL does not exist in catalog answers, so the null mask is
// plain false and the write-back source is empty — a catalog grid is never
// editable.
export const magicGrid = (
  columns: string[],
  rows: (string | any[])[],
  note?: string,
  elapsedMs = 0,
): QueryResult & { note?: string } => ({
  columns: columns.map(name => ({ name, type: 'text', nullable: true })),
  rows: rows as unknown as any[][],
  rowCount: rows.length,
  affectedRows: 0,
  lastInsertId: 0,
  executionTime: elapsedMs,
  note,
});

// rowsFromTables normalizes GetTables' TableInfo[] into name strings, keeping
// only the name (the sidebar already renders types). Each name becomes a
// one-cell row — the grid is row-major.
const namesOf = (list: { name: string }[], pattern: string): string[][] => {
  const p = pattern.toLowerCase();
  return list
    .map(x => x.name)
    .filter(n => !p || n.toLowerCase().includes(p))
    .map(n => [n]);
};

// describeRows flattens a structure read into the \d table: column / type /
// nullable / key / default / comment, one row per column.
export const describeRows = (structure: TableStructure): string[][] =>
  (structure.columns ?? []).map(c => [
    c.name,
    c.type,
    c.nullable ? 'YES' : 'NO',
    c.isPrimaryKey ? 'PK' : '',
    c.defaultValue ?? '',
    c.comment ?? '',
  ]);

// indexRows flattens the structure's indexes for \di.
export const indexRows = (structure: TableStructure): string[][] =>
  (structure.indexes ?? []).map(i => [
    i.name,
    (i.columns ?? []).join(', '),
    i.unique ? 'YES' : 'NO',
    i.primary ? 'PK' : '',
  ]);

// resolveMagicTable answers "which table did \d / \di mean": the argument
// when given, else the tab's open table. Neither present = an explicit
// error, because a silent empty grid would read as "table has no columns".
export const resolveMagicTable = (arg: string, sourceTable: string): string | null => {
  const t = (arg || sourceTable || '').trim();
  return t || null;
};

// parseCopyArgs splits `\copy <表> [格式] [where <谓词>|limit <N>]`. Returns
// null with a reason when the tail cannot be understood — the caller shows
// the reason instead of guessing. `where` keeps the caller's raw text: the
// export path quotes and filters it server-side.
export const parseCopyArgs = (
  arg: string,
): { table: string; format: CopyFormat; where: string } | { error: string } => {
  const parts = arg.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0 || !parts[0]) {
    return { error: '用法: \\copy <表名> [格式] [where <谓词>|limit <N>]' };
  }
  const table = parts.shift() as string;
  let format: CopyFormat = 'csv';
  if (parts.length && isKnownFormat(parts[0])) {
    format = (parts.shift() as string).toLowerCase() as CopyFormat;
  }
  if (parts.length === 0) return { table, format, where: '' };
  const kw = parts.shift()!.toLowerCase();
  if (kw === 'where') return { table, format, where: parts.join(' ') };
  if (kw === 'limit') {
    const n = parts[0] ?? '100';
    if (!/^\d+$/.test(n)) return { error: `\\copy limit 需要数字: ${n}` };
    return { table, format, where: `LIMIT ${n}` };
  }
  return { error: `无法解析 \\copy 参数: ${kw}` };
};

// runMagicCommand executes one parsed command through the fetchers and
// resolves to the grid to display (note carries the command that ran). The
// throw path carries a user-facing message — refused commands never reach
// the database, so "refused" and "failed" are the same surface.
export async function runMagicCommand(
  parsed: { cmd: string; arg: string },
  f: MagicFetchers,
): Promise<QueryResult & { note?: string }> {
  switch (parsed.cmd) {
    case '?':
    case 'help':
      throw new Error(MAGIC_HELP);
    case 'dt': {
      const tables = await f.listTables();
      return magicGrid(['name'], namesOf(tables, parsed.arg), `\\dt${parsed.arg ? ' ' + parsed.arg : ''}`);
    }
    case 'dv': {
      const views = await f.listViews();
      return magicGrid(['name'], namesOf(views, parsed.arg), `\\dv${parsed.arg ? ' ' + parsed.arg : ''}`);
    }
    case 'dn':
    case 'dbs':
      return magicGrid(['schema'], (await f.listSchemas()).map(n => [n]), '\\dn');
    case 'df': {
      const rows = (await f.listRoutines()).map(r => [r.name, r.kind ?? '']);
      return magicGrid(['name', 'type'], rows, '\\df');
    }
    case 'd': {
      const table = resolveMagicTable(parsed.arg, f.sourceTable());
      if (!table) throw new Error('用法: \\d <表名>（省略表名时需已打开一张表）');
      const structure = await f.describe(table);
      return magicGrid(
        ['column', 'type', 'nullable', 'key', 'default', 'comment'],
        describeRows(structure),
        `\\d ${table}`,
      );
    }
    case 'di': {
      const table = resolveMagicTable(parsed.arg, f.sourceTable());
      if (!table) throw new Error('用法: \\di <表名>（省略表名时需已打开一张表）');
      const structure = await f.describe(table);
      return magicGrid(['index', 'columns', 'unique', 'primary'], indexRows(structure), `\\di ${table}`);
    }
    case 'copy': {
      const parsed2 = parseCopyArgs(parsed.arg);
      if ('error' in parsed2) throw new Error(parsed2.error);
      const count = await f.copyTable(parsed2.table, parsed2.format, parsed2.where);
      return magicGrid(
        ['table', 'rows', 'target'],
        [[parsed2.table, String(count), `${parsed2.table}.${parsed2.format === 'excel' ? 'xlsx' : parsed2.format}`]],
        `\\copy ${parsed2.table} ${parsed2.format}${parsed2.where ? ' [' + parsed2.where + ']' : ''}`,
      );
    }
    default:
      throw new Error(`未知魔法命令: \\${parsed.cmd}（输入 \\? 查看帮助）`);
  }
}

export const MAGIC_HELP = [
  '魔法命令（以 \\ 开头，拦截为结果网格，不作为 SQL 执行）：',
  '  \\dt [模式]      列出当前库的表',
  '  \\dv [模式]      列出当前库的视图',
  '  \\dn             列出库 / 模式',
  '  \\d <表名>       描述表结构（省略时用当前打开的表）',
  '  \\di <表名>      列出表的索引',
  '  \\df             列出函数与存储过程',
  '  \\copy <表> [格式] [where <谓词>|limit <N>]   导出表数据',
  '  \\?              显示本帮助',
].join('\n');
