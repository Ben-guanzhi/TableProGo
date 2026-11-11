import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  isMagicCommand,
  parseMagicCommand,
  parseCopyArgs,
  resolveMagicTable,
  describeRows,
  indexRows,
  magicGrid,
  runMagicCommand,
  MAGIC_HELP,
} from '../src/lib/magicCommands.ts';
import type { MagicFetchers } from '../src/lib/magicCommands.ts';
import type { TableStructure } from '../src/types/index.ts';

// ---- parser ----

test('a statement without a leading backslash is not ours', () => {
  assert.equal(parseMagicCommand('SELECT 1'), null);
  assert.equal(parseMagicCommand('  select * from t'), null);
  assert.equal(parseMagicCommand(''), null);
  assert.equal(isMagicCommand('SELECT 1'), false);
});

test('plain \\dt parses with an empty argument', () => {
  assert.deepEqual(parseMagicCommand('\\dt'), { cmd: 'dt', arg: '' });
  assert.deepEqual(parseMagicCommand('  \\dt  '), { cmd: 'dt', arg: '' });
});

test('an argument keeps its inner spacing verbatim', () => {
  // \copy's where-tail is handed to the copy path raw; the parser must not
  // collapse the spaces a predicate carries.
  assert.deepEqual(parseMagicCommand('\\copy orders csv limit 50'), {
    cmd: 'copy',
    arg: 'orders csv limit 50',
  });
});

test('case-insensitive command, unknown and malformed shapes', () => {
  assert.deepEqual(parseMagicCommand('\\DT pat'), { cmd: 'dt', arg: 'pat' });
  assert.deepEqual(parseMagicCommand('\\timing off'), { cmd: 'timing', arg: 'off' });
  assert.equal(parseMagicCommand('\\'), null, 'a bare backslash is not a command');
});

// ---- copy args ----

test('parseCopyArgs defaults to csv with no filter', () => {
  assert.deepEqual(parseCopyArgs('orders'), { table: 'orders', format: 'csv', where: '' });
});

test('parseCopyArgs reads format and limit', () => {
  assert.deepEqual(parseCopyArgs('orders excel limit 500'), {
    table: 'orders',
    format: 'excel',
    where: 'LIMIT 500',
  });
  assert.deepEqual(parseCopyArgs('orders tsv'), { table: 'orders', format: 'tsv', where: '' });
});

test('parseCopyArgs refuses an unknown keyword with a reason', () => {
  const r = parseCopyArgs('orders bogus');
  assert.ok('error' in r);
  assert.match((r as { error: string }).error, /bogus/);
});

test('parseCopyArgs refuses an empty table', () => {
  const r = parseCopyArgs('');
  assert.ok('error' in r);
});

test('parseCopyArgs limit needs a number', () => {
  const r = parseCopyArgs('orders limit abc');
  assert.ok('error' in r);
  assert.match((r as { error: string }).error, /数字/);
});

// ---- table resolution ----

test('resolveMagicTable: argument wins, then the open table, else null', () => {
  assert.equal(resolveMagicTable('users', 'orders'), 'users');
  assert.equal(resolveMagicTable('', 'orders'), 'orders');
  assert.equal(resolveMagicTable('', ''), null);
  assert.equal(resolveMagicTable('   ', '   '), null);
});

// ---- grid builders ----

test('describeRows flattens columns with PK flags and defaults', () => {
  const structure = {
    columns: [
      { name: 'id', type: 'bigint', nullable: false, isPrimaryKey: true, comment: '主键', defaultValue: '' },
      { name: 'note', type: 'text', nullable: true, isPrimaryKey: false, comment: '', defaultValue: '""' },
    ],
    indexes: [],
    foreignKeys: [],
    createTable: '',
  } as unknown as TableStructure;
  assert.deepEqual(describeRows(structure), [
    ['id', 'bigint', 'NO', 'PK', '', '主键'],
    ['note', 'text', 'YES', '', '""', ''],
  ]);
});

test('indexRows joins columns and marks uniqueness', () => {
  const structure = {
    columns: [],
    indexes: [
      { name: 'PRIMARY', kind: 'BTREE', unique: true, primary: true, columns: ['id'] },
      { name: 'idx_email', kind: 'BTREE', unique: false, primary: false, columns: ['email', 'tenant'] },
    ],
    foreignKeys: [],
    createTable: '',
  } as unknown as TableStructure;
  assert.deepEqual(indexRows(structure), [
    ['PRIMARY', 'id', 'YES', 'PK'],
    ['idx_email', 'email, tenant', 'NO', ''],
  ]);
});

test('magicGrid produces a read-only result with text columns', () => {
  const grid = magicGrid(['a', 'b'], [['1', 'x']]);
  assert.equal(grid.rowCount, 1);
  assert.equal(grid.affectedRows, 0);
  assert.deepEqual(grid.columns.map(c => c.name), ['a', 'b']);
  assert.deepEqual(grid.rows, [['1', 'x']]);
});

// ---- executor ----

const stubStructure = (columns: number, indexes: number): TableStructure =>
  ({
    columns: Array.from({ length: columns }, (_, i) => ({
      name: `c${i}`,
      type: 'text',
      nullable: true,
      isPrimaryKey: i === 0,
      comment: '',
      defaultValue: '',
    })),
    indexes: Array.from({ length: indexes }, (_, i) => ({
      name: `idx_${i}`,
      kind: 'BTREE',
      unique: false,
      primary: false,
      columns: ['c0'],
    })),
    foreignKeys: [],
    createTable: '',
  }) as unknown as TableStructure;

const stubFetchers = (over: Partial<MagicFetchers> = {}): MagicFetchers => ({
  listTables: async () => [{ name: 'users' }, { name: 'orders' }],
  listViews: async () => [{ name: 'v_orders', type: 'view' }],
  listSchemas: async () => ['public', 'shop'],
  listRoutines: async () => [{ name: 'fn_total', kind: 'FUNCTION' }],
  describe: async () => stubStructure(2, 1),
  sourceTable: () => 'orders',
  copyTable: async () => 42,
  ...over,
});

test('\\dt lists tables, filtered case-insensitively', async () => {
  const r = await runMagicCommand({ cmd: 'dt', arg: '' }, stubFetchers());
  assert.deepEqual(r.rows, [['users'], ['orders']]);
  const filtered = await runMagicCommand({ cmd: 'dt', arg: 'USERS' }, stubFetchers());
  assert.deepEqual(filtered.rows, [['users']]);
});

test('\\dv lists only views', async () => {
  const r = await runMagicCommand({ cmd: 'dv', arg: '' }, stubFetchers());
  assert.deepEqual(r.rows, [['v_orders']]);
  // The view fetcher is a distinct path: the caller (App) filters GetTables
  // by type, so a stub that ignores the split must not silently pass here.
  const empty = await runMagicCommand(
    { cmd: 'dv', arg: '' },
    stubFetchers({ listViews: async () => [] }),
  );
  assert.equal(empty.rowCount, 0);
});

test('\\dn lists schemas and \\df lists routines', async () => {
  const dn = await runMagicCommand({ cmd: 'dn', arg: '' }, stubFetchers());
  assert.deepEqual(dn.rows, [['public'], ['shop']]);
  const df = await runMagicCommand({ cmd: 'df', arg: '' }, stubFetchers());
  assert.deepEqual(df.rows, [['fn_total', 'FUNCTION']]);
});

test('\\d uses the open table when the argument is absent', async () => {
  const r = await runMagicCommand({ cmd: 'd', arg: '' }, stubFetchers());
  assert.equal(r.rows.length, 2);
  assert.deepEqual(r.rows[0], ['c0', 'text', 'YES', 'PK', '', '']);
});

test('\\d without a table and without an open one refuses with the usage', async () => {
  await assert.rejects(
    () => runMagicCommand({ cmd: 'd', arg: '' }, stubFetchers({ sourceTable: () => '' })),
    /用法: \\d/,
  );
});

test('\\di describes indexes of the named table', async () => {
  const r = await runMagicCommand({ cmd: 'di', arg: 'users' }, stubFetchers());
  assert.deepEqual(r.rows, [['idx_0', 'c0', 'NO', '']]);
});

test('\\copy reports the exported row count through the fetcher', async () => {
  let called: { table: string; format: string; where: string } | null = null;
  const r = await runMagicCommand(
    { cmd: 'copy', arg: 'orders excel limit 50' },
    stubFetchers({
      copyTable: async (table, format, where) => {
        called = { table, format, where };
        return 50;
      },
    }),
  );
  assert.deepEqual(called, { table: 'orders', format: 'excel', where: 'LIMIT 50' });
  assert.deepEqual(r.rows, [['orders', '50', 'orders.xlsx']]);
});

test('an unknown command refuses with the help pointer, never executes', async () => {
  await assert.rejects(
    () => runMagicCommand({ cmd: 'zzz', arg: '' }, stubFetchers()),
    /未知魔法命令: \\zzz/,
  );
});

test('\\? carries the help text', async () => {
  await assert.rejects(() => runMagicCommand({ cmd: '?', arg: '' }, stubFetchers()), /魔法命令/);
  assert.match(MAGIC_HELP, /\\dt/);
  assert.match(MAGIC_HELP, /\\copy/);
});

test('fetcher failures surface as user-facing errors with their message', async () => {
  await assert.rejects(
    () =>
      runMagicCommand(
        { cmd: 'dt', arg: '' },
        stubFetchers({ listTables: async () => { throw new Error('连接已断开'); } }),
      ),
    /连接已断开/,
  );
});
