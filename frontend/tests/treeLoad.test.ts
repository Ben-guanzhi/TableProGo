import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  columnNodes,
  databaseNodes,
  kvKeyNodes,
  loadChildren,
  loadMoreKeys,
  nodeKey,
  schemaNodes,
  tableNodes,
  type Loaders,
} from '../src/datagrid/treeLoad.ts';
import type { TreeNode } from '../src/datagrid/sidebarTree.ts';

// Branch loading decides three things worth pinning: which call answers for a
// node's kind, the key format (the expand/collapse maps are keyed by it, so a
// change silently loses the user's tree), and Redis paging.

const base = { connId: 'c1', dbType: 'postgresql' };

const node = (over: Partial<TreeNode> = {}): TreeNode => ({
  key: 'd|c1|app',
  kind: 'database',
  label: 'app',
  connId: 'c1',
  dbType: 'postgresql',
  database: 'app',
  ...over,
});

function fakeLoaders(over: Partial<Loaders> = {}): Loaders {
  return {
    databases: async () => [],
    schemas: async () => [],
    tables: async () => [],
    columns: async () => [],
    kvKeys: async () => ({ keys: [], cursor: '0', done: true }),
    ...over,
  };
}

test('node keys are stable and prefixed per kind', () => {
  assert.equal(nodeKey.connection('c1'), 'c|c1');
  assert.equal(nodeKey.database('c1', 'app'), 'd|c1|app');
  assert.equal(nodeKey.schema('c1', 'app', 'public'), 's|c1|app|public');
  assert.equal(nodeKey.table('c1', 'app', 'public', 'orders', 'table'), 't|c1|app|public|orders');
  assert.equal(nodeKey.table('c1', 'app', 'public', 'v1', 'view'), 'v|c1|app|public|v1');
  assert.equal(nodeKey.table('c1', '0', '', 'user:1', 'key'), 'k|c1|0|user:1');
  assert.equal(nodeKey.column('c1', 'app', 'public', 'orders', 'id'), 'col|c1|app|public|orders|id');
  assert.equal(nodeKey.more('c1', '0', '42'), 'km|c1|0|42');
});

test('a connection lists its databases and drops empty names', () => {
  const nodes = databaseNodes(base, ['app', '', 'logs']);
  assert.deepEqual(nodes.map(n => n.label), ['app', 'logs']);
  assert.deepEqual(nodes.map(n => n.kind), ['database', 'database']);
  assert.equal(nodes[0].key, 'd|c1|app');
});

test('an engine with schemas lists them instead of objects', async () => {
  const nodes = await loadChildren(
    node(),
    fakeLoaders({
      schemas: async () => ['public', 'audit'],
      tables: async () => {
        throw new Error('tables must not be asked when schemas exist');
      },
    }),
  );
  assert.deepEqual(nodes.map(n => n.label), ['public', 'audit']);
  assert.deepEqual(nodes.map(n => n.kind), ['schema', 'schema']);
});

test('an engine without schemas lists objects directly', async () => {
  const nodes = await loadChildren(
    node({ dbType: 'mysql' }),
    fakeLoaders({
      schemas: async () => [],
      tables: async () => [
        { name: 'orders', type: 'table' },
        { name: 'recent', type: 'view' },
      ],
    }),
  );
  assert.deepEqual(nodes.map(n => n.label), ['orders', 'recent']);
  assert.deepEqual(nodes.map(n => n.kind), ['table', 'view']);
  // The view keeps its own prefix so a rename cannot collide with a table's key.
  assert.equal(nodes[1].key, 'v|c1|app||recent');
});

test('a schema lists its objects', async () => {
  const nodes = await loadChildren(
    node({ key: 's|c1|app|audit', kind: 'schema', schema: 'audit' }),
    fakeLoaders({ tables: async (_c, _d, schema) => (schema === 'audit' ? [{ name: 'events', type: 'table' }] : []) }),
  );
  assert.deepEqual(nodes.map(n => n.label), ['events']);
  assert.equal(nodes[0].key, 't|c1|app|audit|events');
});

test('a table lists its columns with type, key and nullability', async () => {
  const nodes = await loadChildren(
    node({ kind: 'table', schema: 'public', table: 'orders' }),
    fakeLoaders({
      columns: async () => [
        { name: 'id', type: 'bigint', isPrimaryKey: true, nullable: false },
        { name: 'note', type: 'text', isPrimaryKey: false, nullable: true },
      ],
    }),
  );
  assert.deepEqual(nodes.map(n => n.label), ['id', 'note']);
  assert.equal(nodes[0].dataType, 'bigint');
  assert.equal(nodes[0].primaryKey, true);
  assert.equal(nodes[0].nullable, false);
  assert.equal(nodes[1].nullable, true);
});

test('Redis lists keys through the same branch, and a key is a leaf', async () => {
  const nodes = await loadChildren(
    node({ dbType: 'redis' }),
    fakeLoaders({ kvKeys: async () => ({ keys: ['b', 'a'], cursor: '0', done: true }) }),
  );
  assert.deepEqual(nodes.map(n => n.label), ['a', 'b'], 'keys are sorted');
  assert.deepEqual(nodes.map(n => n.kind), ['key', 'key']);
  // A key node carries the table field so value tabs can address it.
  assert.equal(nodes[0].table, 'a');
});

test('Redis paging de-duplicates, sorts, and keeps the cursor row', () => {
  const previous = kvKeyNodes({ connId: 'c1', dbType: 'redis', database: '0' }, { keys: ['b', 'a'], cursor: '7', done: false }, []);
  assert.deepEqual(previous.map(n => n.label), ['a', 'b', '加载更多键…']);
  const more = previous[previous.length - 1];
  assert.equal(more.kind, 'more');
  assert.equal(more.cursor, '7');
  assert.equal(more.key, 'km|c1|0|7');
});

test('a finished scan drops the "load more" row', () => {
  const nodes = kvKeyNodes({ connId: 'c1', dbType: 'redis', database: '0' }, { keys: ['a'], cursor: '0', done: true }, []);
  assert.deepEqual(nodes.map(n => n.kind), ['key']);
});

test('loadMoreKeys appends the page and replaces the cursor row', async () => {
  const first = kvKeyNodes(
    { connId: 'c1', dbType: 'redis', database: '0' },
    { keys: ['a'], cursor: '7', done: false },
    [],
  );
  const more = first.find(n => n.kind === 'more') as TreeNode;
  // A SCAN page may repeat a key; the append must not duplicate it.
  const next = await loadMoreKeys(
    more,
    first,
    fakeLoaders({ kvKeys: async () => ({ keys: ['a', 'c'], cursor: '0', done: true }) }),
  );
  assert.deepEqual(next.map(n => n.label), ['a', 'c']);
});

test('a column node is a leaf: loading it yields nothing', async () => {
  const nodes = await loadChildren(
    node({ kind: 'column', schema: 'public', table: 'orders', dataType: 'bigint' }),
    fakeLoaders({
      columns: async () => {
        throw new Error('a column must not be asked for children');
      },
    }),
  );
  assert.deepEqual(nodes, []);
});

test('the column builder keeps the parent addressing', () => {
  const nodes = columnNodes(
    { connId: 'c1', dbType: 'postgresql', database: 'app', schema: 'public', table: 'orders' },
    [{ name: 'id', type: 'bigint', isPrimaryKey: true, nullable: false }],
  );
  assert.equal(nodes[0].database, 'app');
  assert.equal(nodes[0].schema, 'public');
  assert.equal(nodes[0].table, 'orders');
  assert.equal(nodes[0].key, 'col|c1|app|public|orders|id');
});

test('schema builder carries the database through', () => {
  const nodes = schemaNodes({ connId: 'c1', dbType: 'postgresql', database: 'app' }, ['public']);
  assert.equal(nodes[0].database, 'app');
  assert.equal(nodes[0].schema, 'public');
});

test('table builder marks views apart from tables', () => {
  const nodes = tableNodes(base, 'public', [
    { name: 'orders', type: 'table' },
    { name: 'v', type: 'view' },
  ]);
  assert.deepEqual(nodes.map(n => n.kind), ['table', 'view']);
});
