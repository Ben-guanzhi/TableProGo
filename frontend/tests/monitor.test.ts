import assert from 'node:assert/strict';
import { test } from 'node:test';

import { latestValue, sparkPoints, sparkSummary, type MonitorState } from '../src/lib/monitor.ts';

// W-M1: the client-side ring helpers. The panel must distinguish "no data yet"
// from "value 0", and the sparkline shape matches chart.ts's point contract.

const mk = (values: number[], dropped = 0): MonitorState => ({
  host: 'web-1',
  tag: 'monitor/web-1',
  samples: values.map(v => ({ time: '2026-11-13T00:00:00Z', value: v })),
  dropped,
});

test('latestValue reads the newest sample', () => {
  assert.equal(latestValue(mk([1, 2, 3])), 3);
});

test('latestValue returns null for an empty ring (not zero)', () => {
  // A zero line would lie: no samples is "no data", not "value 0".
  assert.equal(latestValue(mk([])), null);
});

test('sparkPoints maps the ring into chart points', () => {
  const pts = sparkPoints(mk([4, 5]));
  assert.deepEqual(pts, [{ label: '1', value: 4 }, { label: '2', value: 5 }]);
});

test('sparkSummary shows the latest value and the ring fill', () => {
  assert.equal(sparkSummary(mk([1, 2])), '2.00 · 环 2');
});

test('sparkSummary reports evictions once they exist', () => {
  assert.equal(sparkSummary(mk([1, 2, 3, 4, 5], 7)), '5.00 · 环 5 · 驱逐 7');
});

test('sparkSummary says waiting when there is nothing yet', () => {
  assert.equal(sparkSummary(mk([])), '等待采样…');
});