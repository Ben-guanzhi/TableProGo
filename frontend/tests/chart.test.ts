import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  CHART_HEIGHT,
  CHART_WIDTH,
  asNumber,
  barRects,
  linePath,
  pickChart,
  truncateLabel,
} from '../src/datagrid/chart.ts';

test('asNumber accepts numbers and numeric text, rejects the rest', () => {
  assert.equal(asNumber(3), 3);
  assert.equal(asNumber('3.5'), 3.5);
  assert.equal(asNumber(' -2 '), -2);
  assert.equal(asNumber('abc'), null);
  assert.equal(asNumber('12abc'), null);
  assert.equal(asNumber(null), null);
  assert.equal(asNumber(undefined), null);
});

test('pickChart needs at least two rows', () => {
  assert.equal(pickChart(['a'], [[1]], 'bar'), null);
  assert.equal(pickChart(['a'], [], 'bar'), null);
});

test('pickChart finds the value and label columns', () => {
  const data = pickChart(['region', 'total'], [['east', 10], ['west', 20]]);
  assert.ok(data);
  assert.equal(data!.valueColumn, 'total');
  assert.equal(data!.labelColumn, 'region');
  assert.deepEqual(data!.points, [
    { label: 'east', value: 10 },
    { label: 'west', value: 20 },
  ]);
});

test('pickChart refuses a partly-numeric column', () => {
  // "12" and "abc" in the same column is a text column that happens to hold a
  // number; charting it would silently drop rows.
  const data = pickChart(['v'], [[12], ['abc']]);
  assert.equal(data, null);
});

test('pickChart falls back to the row index when no text column exists', () => {
  const data = pickChart(['a', 'b'], [[1, 2], [3, 4]]);
  assert.ok(data);
  assert.equal(data!.labelColumn, '#');
  assert.deepEqual(data!.points.map(p => p.label), ['1', '2']);
});

test('barRects keeps the axis at zero for positive series', () => {
  const data = pickChart(['k', 'v'], [['a', 5], ['b', 10]])!;
  const rects = barRects(data);
  // The taller bar must reach higher (smaller y) and both must sit above the
  // baseline; an axis that did not start at zero would make 5 look like half of 10
  // by construction, which is the exaggeration this avoids.
  assert.ok(rects[1].y < rects[0].y);
  assert.ok(rects[0].h < rects[1].h);
  for (const r of rects) {
    assert.ok(r.y >= 0 && r.y + r.h <= CHART_HEIGHT);
    assert.ok(r.x >= 0 && r.x + r.w <= CHART_WIDTH);
  }
});

test('barRects hangs negative bars below zero', () => {
  const data = pickChart(['k', 'v'], [['a', -5], ['b', 5]])!;
  const rects = barRects(data);
  // One bar is above the zero line and one below: their tops differ.
  assert.notEqual(rects[0].y, rects[1].y);
  assert.ok(rects[0].h > 0 && rects[1].h > 0);
});

test('linePath emits a path with one point per row', () => {
  const data = pickChart(['k', 'v'], [['a', 1], ['b', 2], ['c', 3]], 'line')!;
  const path = linePath(data);
  assert.ok(path.startsWith('M '));
  assert.equal((path.match(/L /g) ?? []).length, 2);
});

test('truncateLabel shortens with an ellipsis', () => {
  assert.equal(truncateLabel('short'), 'short');
  assert.equal(truncateLabel('a-very-long-label', 8).length, 8);
  assert.ok(truncateLabel('a-very-long-label', 8).endsWith('…'));
});
