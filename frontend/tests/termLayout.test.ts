import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  closePaneRoot,
  countLeaves,
  deserialize,
  findLeaf,
  findLeafByChannel,
  flatten,
  newLeaf,
  serialize,
  setLeafChannel,
  splitPane,
} from '../src/lib/termLayout.ts';

// W-T2: the pane tree's invariants. The tests read like the plan's bullet —
// "termLayout 纯函数树" — because every mutation returns a NEW tree and the
// render order (flatten) is the only observable layout.

test('a fresh tree is one unconnected leaf', () => {
  const tree = newLeaf('p1');
  assert.equal(countLeaves(tree), 1);
  assert.equal(findLeaf(tree, 'p1')?.channelId, null);
});

test('split adds a sibling leaf; both stay addressable', () => {
  let tree = newLeaf('p1');
  tree = splitPane(tree, 'p1', 'h', 'p2');
  assert.equal(countLeaves(tree), 2);
  assert.deepEqual(flatten(tree).map(l => l.id), ['p1', 'p2']);
  // The new leaf is unconnected — splitting is a layout gesture, not a session.
  assert.equal(findLeaf(tree, 'p2')?.channelId, null);
});

test('splitting twice builds a multi-level tree in render order', () => {
  let tree = newLeaf('p1');
  tree = splitPane(tree, 'p1', 'h', 'p2');
  tree = splitPane(tree, 'p2', 'v', 'p3');
  assert.equal(countLeaves(tree), 3);
  assert.deepEqual(flatten(tree).map(l => l.id), ['p1', 'p2', 'p3']);
});

test('split with a stale id leaves the tree unchanged', () => {
  const tree = newLeaf('p1');
  const after = splitPane(tree, 'no-such-pane', 'h', 'p2');
  assert.equal(countLeaves(after), 1);
  assert.deepEqual(after, tree);
});

test('close removes the leaf and collapses a single-child split', () => {
  let tree = newLeaf('p1');
  tree = splitPane(tree, 'p1', 'h', 'p2');
  tree = splitPane(tree, 'p2', 'v', 'p3');
  // Close p3: p2's split had two children (p2, p3) → one left (p2) → collapses.
  tree = closePaneRoot(tree, 'p3');
  assert.equal(countLeaves(tree), 2);
  assert.deepEqual(flatten(tree).map(l => l.id), ['p1', 'p2']);
  // Close p2: the root split collapses to the bare p1 leaf.
  tree = closePaneRoot(tree, 'p2');
  assert.equal(countLeaves(tree), 1);
  assert.deepEqual(flatten(tree).map(l => l.id), ['p1']);
});

test('closing a middle leaf keeps the siblings around it', () => {
  let tree = newLeaf('p1');
  tree = splitPane(tree, 'p1', 'h', 'p2');
  tree = splitPane(tree, 'p1', 'v', 'p3');
  // Tree: v-split(p1, h-split(p2, p3))? No — split(p1,'v','p3') replaces the
  // LEAF p1: v-split(p1', p3) where p1' is the same id... the original p1 leaf
  // is re-created by the split, so closing p1 removes THAT leaf.
  tree = closePaneRoot(tree, 'p1');
  const ids = flatten(tree).map(l => l.id);
  assert.ok(!ids.includes('p1'), `p1 must be gone: ${ids}`);
  assert.equal(ids.length, 2);
});

test('closing the last leaf yields one fresh empty leaf (the panel stays alive)', () => {
  const tree = newLeaf('only');
  const after = closePaneRoot(tree, 'only');
  assert.equal(countLeaves(after), 1);
  assert.equal(flatten(after)[0].id, 'only-reborn');
  assert.equal(flatten(after)[0].channelId, null);
});

test('setLeafChannel rebinds exactly one leaf', () => {
  let tree = newLeaf('p1');
  tree = splitPane(tree, 'p1', 'h', 'p2');
  tree = setLeafChannel(tree, 'p2', 'ch-9');
  assert.equal(findLeaf(tree, 'p1')?.channelId, null);
  assert.equal(findLeaf(tree, 'p2')?.channelId, 'ch-9');
  tree = setLeafChannel(tree, 'p2', null);
  assert.equal(findLeaf(tree, 'p2')?.channelId, null);
});

test('findLeafByChannel locates the pane rendering a channel', () => {
  let tree = newLeaf('p1');
  tree = splitPane(tree, 'p1', 'h', 'p2');
  tree = setLeafChannel(tree, 'p2', 'ch-9');
  assert.equal(findLeafByChannel(tree, 'ch-9')?.id, 'p2');
  assert.equal(findLeafByChannel(tree, 'ch-absent'), null);
});

test('serialize/deserialize round-trips the tree', () => {
  let tree = newLeaf('p1');
  tree = splitPane(tree, 'p1', 'h', 'p2');
  tree = setLeafChannel(tree, 'p2', 'ch-9');
  const back = deserialize(serialize(tree));
  assert.deepEqual(flatten(back!), flatten(tree));
});

test('deserialize refuses garbage instead of throwing', () => {
  assert.equal(deserialize('not json'), null);
  assert.equal(deserialize('42'), null);
});
