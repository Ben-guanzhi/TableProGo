import assert from 'node:assert/strict';
import { test } from 'node:test';

// Tests for src/datagrid/virtual.ts â€?window arithmetic contracts (K-4).
// The implementation is plain JS-compatible TS; import via a tiny loader that
// strips types, mirroring how other grid tests consume pure modules.

import { computeVisibleRange, ROW_HEIGHT, OVERSCAN } from '../src/datagrid/virtual.ts';

test('window span is independent of total (10k vs 100k rows)', () => {
  const small = computeVisibleRange(5000, 600, 40, 10_000);
  const large = computeVisibleRange(5000, 600, 40, 100_000);
  assert.equal(small.i1 - small.i0, large.i1 - large.i0, 'same scroll â†?same span');
});

test('empty result yields an inverted empty window', () => {
  const w = computeVisibleRange(0, 600, 40, 0);
  assert.deepEqual([w.i0, w.i1, w.topPad, w.bottomPad], [0, -1, 0, 0]);
});

test('window clamps at both ends and never inverts', () => {
  const top = computeVisibleRange(0, 600, 40, 50);
  assert.equal(top.i0, 0);
  const bottom = computeVisibleRange(1_000_000, 600, 40, 50);
  assert.equal(bottom.i1, 49);
  assert.ok(bottom.i0 <= bottom.i1, 'no inverted window');
  const tiny = computeVisibleRange(100, 600, 40, 2);
  assert.deepEqual([tiny.i0, tiny.i1], [0, 1]);
});

test('padding sums to unrendered height (scrollbar honesty)', () => {
  const total = 1_000;
  const w = computeVisibleRange(5_321, 600, 40, total);
  const rendered = w.i1 - w.i0 + 1;
  assert.equal(w.topPad, w.i0 * ROW_HEIGHT);
  assert.equal(w.bottomPad, (total - 1 - w.i1) * ROW_HEIGHT);
  assert.equal(w.topPad / ROW_HEIGHT + rendered + w.bottomPad / ROW_HEIGHT, total);
});

test('header height shifts the window, not its size', () => {
  const withHeader = computeVisibleRange(600, 600, 40, 1_000);
  const noHeader = computeVisibleRange(560, 600, 0, 1_000);
  // Same content offset (600-40 == 560-0) â†?same first row.
  assert.equal(withHeader.i0, noHeader.i0);
});

test('overscan widens the window symmetrically', () => {
  const w = computeVisibleRange(ROW_HEIGHT * 100, 0, 0, 1_000, ROW_HEIGHT, 8);
  assert.equal(w.i0, 100 - OVERSCAN);
  assert.equal(w.i1, 100 + OVERSCAN);
});

test('viewport inside one row still renders that row', () => {
  const w = computeVisibleRange(ROW_HEIGHT * 7 + 5, 10, 0, 1_000);
  assert.ok(w.i0 <= 7 && w.i1 >= 7, 'row 7 must be inside the window');
});
