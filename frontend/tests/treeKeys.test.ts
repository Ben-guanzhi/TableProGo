import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  canExpand,
  decideTreeKey,
  isMenuNode,
  visibleTreeRows,
} from '../src/datagrid/treeKeys.ts';
import type { FlatRow, TreeNode } from '../src/datagrid/sidebarTree.ts';

// The arrow keys have to agree with what is painted: ArrowRight expands a closed
// branch but steps into an open one, ArrowLeft collapses an open branch but jumps
// to the parent of a closed one. These are the rules that used to be inline in the
// component's key handler.

const node = (key: string, kind: TreeNode['kind'] = 'table'): TreeNode => ({
  key,
  kind,
  label: key,
  connId: 'c1',
  dbType: 'postgresql',
});

// d (database)
//   t1 (table)
//     col (column)
//   t2 (table)
const t1 = node('t1');
const t2 = node('t2');
const col = node('col', 'column');
const rows: FlatRow[] = [
  { node: node('d', 'database'), depth: 0 },
  { node: t1, depth: 1 },
  { node: col, depth: 2 },
  { node: t2, depth: 1 },
];

const press = (key: string, target: TreeNode, depth: number, open = false) =>
  decideTreeKey({ key }, target, depth, rows, open);

test('down and up move one painted row at a time', () => {
  assert.deepEqual(press('ArrowDown', t1, 1), { kind: 'focus', key: 'col' });
  assert.deepEqual(press('ArrowUp', col, 2), { kind: 'focus', key: 't1' });
  // The ends clamp instead of wrapping.
  assert.deepEqual(press('ArrowDown', t2, 1), { kind: 'focus', key: 't2' });
  assert.deepEqual(press('ArrowUp', rows[0].node, 0), { kind: 'focus', key: 'd' });
});

test('Home and End jump to the ends of the painted list', () => {
  assert.deepEqual(press('Home', col, 2), { kind: 'focus', key: 'd' });
  assert.deepEqual(press('End', rows[0].node, 0), { kind: 'focus', key: 't2' });
});

test('right opens a closed branch and steps into an open one', () => {
  assert.deepEqual(press('ArrowRight', t1, 1, false), { kind: 'toggle', node: t1 });
  assert.deepEqual(press('ArrowRight', t1, 1, true), { kind: 'focus', key: 'col' });
});

test('right does nothing on a leaf', () => {
  assert.deepEqual(press('ArrowRight', col, 2, false), { kind: 'none' });
  assert.deepEqual(press('ArrowRight', node('k', 'key'), 1, false), { kind: 'none' });
  assert.deepEqual(press('ArrowRight', node('m', 'more'), 1, false), { kind: 'none' });
});

test('left collapses an open branch and jumps to the parent of a closed one', () => {
  assert.deepEqual(press('ArrowLeft', t1, 1, true), { kind: 'toggle', node: t1 });
  // Closed: the parent is the nearest earlier row one level up. t2 sits under the
  // database, so that is where left arrow goes (not to its sibling t1).
  assert.deepEqual(press('ArrowLeft', t2, 1, false), { kind: 'focus', key: 'd' });
  assert.deepEqual(press('ArrowLeft', col, 2, false), { kind: 'focus', key: 't1' });
  // A root row has no parent: nothing to do.
  assert.deepEqual(press('ArrowLeft', rows[0].node, 0, false), { kind: 'none' });
});

test('a leaf that is "open" still jumps to its parent', () => {
  assert.deepEqual(press('ArrowLeft', col, 2, true), { kind: 'focus', key: 't1' });
});

test('Enter and Space activate, ContextMenu asks for the menu', () => {
  assert.deepEqual(press('Enter', t1, 1), { kind: 'activate', node: t1 });
  assert.deepEqual(press(' ', t1, 1), { kind: 'activate', node: t1 });
  assert.deepEqual(press('ContextMenu', t1, 1), { kind: 'contextMenu', node: t1 });
  assert.deepEqual(press('Tab', t1, 1), { kind: 'none' });
});

test('an unknown row falls back to the first entry instead of crashing', () => {
  const stale = node('gone');
  // Index falls back to 0, so "down" is the second painted row.
  assert.deepEqual(press('ArrowDown', stale, 1), { kind: 'focus', key: 't1' });
});

test('canExpand excludes leaves and isMenuNode excludes columns', () => {
  assert.equal(canExpand(t1), true);
  assert.equal(canExpand(col), false);
  assert.equal(canExpand(node('k', 'key')), false);
  assert.equal(canExpand(node('m', 'more')), false);
  assert.equal(isMenuNode('column'), false);
  assert.equal(isMenuNode('more'), false);
  assert.equal(isMenuNode('schema'), true);
});

test('filtering keeps the path to a hit open and hides other branches', () => {
  const children = {
    d: [t1, t2],
    t1: [col],
  };
  const expanded = { d: true };
  const tree = [node('d', 'database')];

  // "t2" matches a leaf, so its parent branch must be shown open.
  const filtered = visibleTreeRows(tree, children, expanded, 't2');
  assert.deepEqual(filtered.map(r => r.node.key), ['d', 't2']);

  // A column hit keeps the whole path.
  const deep = visibleTreeRows(tree, children, expanded, 'col');
  assert.deepEqual(deep.map(r => r.node.key), ['d', 't1', 'col']);

  // Without a filter, the user's own expand state decides: d is open so both
  // tables show, and t1 is closed so its column does not.
  assert.deepEqual(visibleTreeRows(tree, children, expanded, '').map(r => r.node.key), ['d', 't1', 't2']);
});
