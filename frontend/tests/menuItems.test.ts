import assert from 'node:assert/strict';
import { test } from 'node:test';

import { menuEditorFor, menuItemsFor, menuTitle } from '../src/datagrid/menuItems.ts';
import type { TreeNode } from '../src/datagrid/sidebarTree.ts';

// The context menu's rules: which items a node gets, which inline editor
// replaces them, and the breadcrumb above. Kept as data so the component only
// maps ids onto labels.

const node = (over: Partial<TreeNode> = {}): TreeNode => ({
  key: 't|c1|app|public|orders',
  kind: 'table',
  label: 'orders',
  connId: 'c1',
  dbType: 'postgresql',
  database: 'app',
  schema: 'public',
  table: 'orders',
  ...over,
});

test('a table can be emptied, dropped, renamed, copied and refreshed', () => {
  assert.deepEqual(menuItemsFor(node()), ['copyDdl', 'rename', 'truncate', 'drop', 'refresh']);
});

test('a view has no truncate (it holds no rows of its own)', () => {
  assert.deepEqual(menuItemsFor(node({ kind: 'view' })), ['copyDdl', 'rename', 'drop', 'refresh']);
});

test('a schema is a container: it gains a table, an ER diagram, routines, enums and backup', () => {
  assert.deepEqual(
    menuItemsFor(node({ kind: 'schema', key: 's|1', label: 'public', table: undefined })),
    ['newTable', 'er', 'routines', 'enums', 'backup', 'refresh'],
  );
});

test('a database is a container too', () => {
  assert.deepEqual(
    menuItemsFor(node({ kind: 'database', key: 'd|1', label: 'app', schema: undefined, table: undefined })),
    ['newTable', 'er', 'routines', 'enums', 'backup', 'refresh'],
  );
});

// Functions/procedures exist on every SQL engine but SQLite; Redis has none.
test('routines appear exactly where stored routines exist', () => {
  for (const dbType of ['postgresql', 'mysql', 'sqlserver', 'oracle']) {
    const items = menuItemsFor(node({ kind: 'database', key: 'd|4', label: 'app', schema: undefined, table: undefined, dbType }));
    assert.equal(items.includes('routines'), true, `${dbType} should offer routines`);
  }
  for (const dbType of ['sqlite', 'redis']) {
    const items = menuItemsFor(node({ kind: 'database', key: 'd|5', label: 'app', schema: undefined, table: undefined, dbType }));
    assert.equal(items.includes('routines'), false, `${dbType} must not offer routines`);
  }
});

// User-defined enum types are PostgreSQL-only.
test('enums appear only on PostgreSQL', () => {
  for (const dbType of ['postgresql']) {
    const items = menuItemsFor(node({ kind: 'schema', key: 's|2', label: 'public', table: undefined, dbType }));
    assert.equal(items.includes('enums'), true, `${dbType} should offer enums`);
  }
  for (const dbType of ['mysql', 'sqlserver', 'oracle', 'sqlite', 'redis']) {
    const items = menuItemsFor(node({ kind: 'schema', key: 's|3', label: 'public', table: undefined, dbType }));
    assert.equal(items.includes('enums'), false, `${dbType} must not offer enums`);
  }
});

// Only the two engines with an official single-file dump tool offer backup.
test('backup appears for pg/mysql containers, never for sqlite/redis', () => {
  for (const dbType of ['postgresql', 'mysql']) {
    const items = menuItemsFor(node({ kind: 'database', key: 'd|2', label: 'app', schema: undefined, table: undefined, dbType }));
    assert.equal(items.includes('backup'), true, `${dbType} should offer backup`);
  }
  for (const dbType of ['sqlite', 'redis']) {
    const items = menuItemsFor(node({ kind: 'database', key: 'd|3', label: 'app', schema: undefined, table: undefined, dbType }));
    assert.equal(items.includes('backup'), false, `${dbType} must not offer backup`);
  }
});

test('a Redis key never offers DDL actions', () => {
  const items = menuItemsFor(node({ kind: 'key', key: 'k|1', label: 'user:1', database: '0' }));
  assert.deepEqual(items, ['keyTtl', 'keyRename', 'keyDelete', 'keyRefresh']);
  for (const forbidden of ['drop', 'truncate', 'copyDdl', 'newTable'] as const) {
    assert.equal(items.includes(forbidden as never), false, `${forbidden} must not appear for a key`);
  }
});

test('a draft replaces the item list with its editor', () => {
  assert.equal(menuEditorFor(node(), 'orders_v2', null), 'rename');
  // TTL only makes sense for a key, so a stray ttlDraft on a table is ignored.
  assert.equal(menuEditorFor(node(), null, '60'), null);
  assert.equal(menuEditorFor(node({ kind: 'key' }), null, '60'), 'ttl');
  assert.equal(menuEditorFor(node(), null, null), null);
});

test('the breadcrumb is qualified for objects and key-prefixed for keys', () => {
  assert.equal(menuTitle(node()), 'app.public.orders');
  assert.equal(menuTitle(node({ kind: 'database', schema: undefined })), 'app.orders');
  assert.equal(menuTitle(node({ kind: 'key', label: 'user:1', database: '0' })), '0.user:1');
});
