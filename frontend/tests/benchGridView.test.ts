import { performance } from 'node:perf_hooks';
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { buildView, cellText, isNullValue } from '../src/gridView.ts';

// 客户端侧性能基准：结果网格的排序+过滤纯逻辑（DOM 渲染不在纯逻辑层）。
// node:test 没有原生 bench，用 perf_hooks 计时 + 固定迭代次数，结果用
// diagnostic 输出（与 §315 的 Go 基准同一口径：ns/op + 分配估计）。

// 20 列 × 5000 行，混合类型，模拟真实宽结果。
function makeRows(n: number, cols: number): any[][] {
  const rows: any[][] = [];
  for (let i = 0; i < n; i++) {
    const row: any[] = [];
    for (let j = 0; j < cols; j++) {
      switch (j % 4) {
        case 0: row.push(`row${String(i).padStart(6, '0')}`); break;
        case 1: row.push(String(i * 7 + j)); break;
        case 2: row.push('active'); break;
        case 3: row.push(null); break;
      }
    }
    rows.push(row);
  }
  return rows;
}

function cols(names: string[]) {
  return names.map((name) => ({ name, type: 'text', isDecimal: false }));
}

function timeIt(fn: () => void, iters: number): number {
  const start = performance.now();
  for (let i = 0; i < iters; i++) fn();
  const ms = (performance.now() - start) / iters;
  return ms * 1e6; // ns/op
}

test('bench: buildView filter+sort 5000×20', (t) => {
  const rows = makeRows(5000, 20);
  const names = Array.from({ length: 20 }, (_, i) => `c${i}`);
  const columns = cols(names);
  const filters = { c2: { op: 'eq', value: 'active' }, c0: { op: 'contains', value: 'row' } };
  const sort = { column: 'c1', dir: 'asc' as const };

  // 正确性预检：基准测的是正确代码。
  const view = buildView(rows, columns, filters, sort);
  assert.ok(view.length > 0 && view.length <= rows.length);
  assert.ok(cellText(rows[view[0]][1]) === String(view[0] * 7 + 1));

  const ns = timeIt(() => {
    const v = buildView(rows, columns, filters, sort);
    if (v.length !== view.length) throw new Error('unstable');
  }, 50);

  t.diagnostic(`BenchmarkBuildView5000x20 filter+sort: ${Math.round(ns).toLocaleString()} ns/op (50 iters)`);
});

test('bench: buildView filter-only 5000×20', (t) => {
  const rows = makeRows(5000, 20);
  const names = Array.from({ length: 20 }, (_, i) => `c${i}`);
  const columns = cols(names);
  const filters = { c2: { op: 'eq', value: 'active' } };

  const view = buildView(rows, columns, filters, null);
  assert.ok(view.length > 0);

  const ns = timeIt(() => {
    buildView(rows, columns, filters, null);
  }, 50);

  t.diagnostic(`BenchmarkBuildView5000x20 filter-only: ${Math.round(ns).toLocaleString()} ns/op`);
});

test('bench: cellKey+cellText (render pre-pass) 5000×20', (t) => {
  const rows = makeRows(5000, 20);
  const ns = timeIt(() => {
    for (const row of rows) for (const v of row) {
      if (isNullValue(v)) continue;
      cellText(v);
    }
  }, 20);

  t.diagnostic(`BenchmarkCellText5000x20: ${Math.round(ns).toLocaleString()} ns/op (100k cells)`);
});
