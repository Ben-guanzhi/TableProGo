import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  descendantKeys,
  emptySidebarState,
  flushRows,
  sidebarReducer,
  type TreeNode,
} from '../src/datagrid/sidebarTree.ts';

const node = (key: string, kind: TreeNode['kind'] = 'table'): TreeNode => ({
  key,
  kind,
  label: key,
  connId: 'c1',
  dbType: 'postgresql',
});

test('loading clears a previous error for the same node', () => {
  let state = sidebarReducer(emptySidebarState, { type: 'failed', key: 'd|1', message: 'offline' });
  assert.equal(state.errors['d|1'], 'offline');
  state = sidebarReducer(state, { type: 'loading', key: 'd|1' });
  assert.equal(state.loading['d|1'], true);
  assert.equal(state.errors['d|1'], undefined);
  // The maps are replaced, not mutated: the previous state is still intact.
  assert.equal(emptySidebarState.loading['d|1'], undefined);
});

test('a failed load stops the spinner and keeps the message', () => {
  const state = sidebarReducer(
    sidebarReducer(emptySidebarState, { type: 'loading', key: 'd|1' }),
    { type: 'failed', key: 'd|1', message: 'no rights' },
  );
  assert.equal(state.loading['d|1'], false);
  assert.equal(state.errors['d|1'], 'no rights');
});

test('opening a menu drops the previous drafts and key info', () => {
  let state = sidebarReducer(emptySidebarState, { type: 'renameDraft', value: 'x' });
  state = sidebarReducer(state, { type: 'ttlDraft', value: '60' });
  state = sidebarReducer(state, { type: 'keyInfo', info: { type: 'hash', ttl: -1 } });
  state = sidebarReducer(state, { type: 'menuOpen', node: node('k|1', 'key'), x: 10, y: 20 });
  assert.deepEqual(state.menu, { node: node('k|1', 'key'), x: 10, y: 20 });
  assert.equal(state.renameDraft, null);
  assert.equal(state.ttlDraft, null);
  assert.equal(state.keyInfo, null);
  // Closing clears the anchor too.
  assert.equal(sidebarReducer(state, { type: 'menuClose' }).menu, null);
});

test('a refreshed branch drops its children and collapses descendants', () => {
  let state = emptySidebarState;
  state = sidebarReducer(state, { type: 'loaded', key: 'd|1', rows: [node('t|1'), node('t|2')] });
  state = sidebarReducer(state, { type: 'loaded', key: 't|1', rows: [node('col|1', 'column')] });
  state = sidebarReducer(state, { type: 'expandedSet', key: 'd|1', expanded: true });
  state = sidebarReducer(state, { type: 'expandedSet', key: 't|1', expanded: true });

  const keys = descendantKeys(state.children, 'd|1');
  assert.deepEqual(keys.sort(), ['col|1', 't|1', 't|2']);

  state = sidebarReducer(state, { type: 'subtreeCleared', key: 'd|1', keys });
  assert.equal(state.children['d|1'], undefined);
  assert.equal(state.children['t|1'], undefined);
  assert.equal(state.expanded['d|1'], undefined);
  assert.equal(state.expanded['t|1'], undefined);
});

test('flushRows walks only expanded branches, in order', () => {
  const children = { d: [node('t1'), node('t2')], t1: [node('col', 'column')] };
  const collapsed = flushRows([node('d', 'database')], children, {});
  assert.deepEqual(collapsed.map(r => r.node.key), ['d']);

  const open = flushRows([node('d', 'database')], children, { d: true, t1: true });
  assert.deepEqual(open.map(r => r.node.key), ['d', 't1', 'col', 't2']);
  assert.deepEqual(open.map(r => r.depth), [0, 1, 2, 1]);
});

test('busy gates destructive actions and resets after', () => {
  const busy = sidebarReducer(emptySidebarState, { type: 'busy', busy: true });
  assert.equal(busy.busy, true);
  assert.equal(sidebarReducer(busy, { type: 'busy', busy: false }).busy, false);
});
