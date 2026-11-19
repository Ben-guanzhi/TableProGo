import { performance } from 'node:perf_hooks';
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { buildView, cellText, isNullValue } from '../src/gridView.ts';

// 回归分析（§317）：把 buildView 注释里声明的三项优化换成"旧写法"，
// 在同一份数据上 A/B 对照，量化优化收益。产品代码不动——旧实现在
// 本文件里手写，作为"如果优化被意外移除会退化多少"的回归基线。
//
// 三项优化：
//   A. 过滤列索引 + 测试函数解析一次（旧：每行 findIndex + 构造 test）
//   B. 排序 key 每行缓存一次（旧：比较器里每行重算 cellText）
//   C. 两者合起来（当前 buildView）

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

const COLUMNS = Array.from({ length: 20 }, (_, i) => ({
  name: `c${i}`, type: 'text', isDecimal: false,
}));

// 旧的过滤写法：每行都重新 findIndex 列、重新构造测试函数。
function filterOldWay(rows: any[][], filters: Record<string, { op: string; value: string }>): number[] {
  const all = rows.map((_, i) => i);
  return all.filter(i => Object.entries(filters).every(([name, f]) => {
    const index = COLUMNS.findIndex(c => c.name === name); // 每行重算
    if (index < 0) return false;
    const needle = f.value.trim().toLowerCase(); // 每行重算
    const v = rows[i][index];
    if (isNullValue(v)) return false;
    const text = cellText(v).toLowerCase();
    return f.op === 'contains' ? text.includes(needle) : (text === needle);
  }));
}

// 旧的排序写法：比较器里每行重算 cellText（不缓存 key）。
function sortOldWay(kept: number[], rows: any[][]): number[] {
  return kept.sort((x, y) => {
    const vx = cellText(rows[x][1]); // 每次比较重算
    const vy = cellText(rows[y][1]);
    return vx < vy ? -1 : vx > vy ? 1 : 0;
  });
}

function timeIt(fn: () => void, iters: number): number {
  const start = performance.now();
  for (let i = 0; i < iters; i++) fn();
  return ((performance.now() - start) / iters) * 1e6; // ns/op
}

const FILTERS = { c2: { op: 'eq', value: 'active' }, c0: { op: 'contains', value: 'row' } };
const SORT = { column: 'c1', dir: 'asc' as const };

test('regression: column/test resolution hoist (A/B)', (t) => {
  const rows = makeRows(5000, 20);
  // 正确性：两种写法结果一致。
  const expected = buildView(rows, COLUMNS, FILTERS, null);
  const old = filterOldWay(rows, FILTERS);
  assert.deepEqual(old, expected);

  const oldNs = timeIt(() => { filterOldWay(rows, FILTERS); }, 30);
  const newNs = timeIt(() => { buildView(rows, COLUMNS, FILTERS, null); }, 30);
  t.diagnostic(
    `A 列索引/测试解析：旧 ${Math.round(oldNs).toLocaleString()} ns/op → 新 ${Math.round(newNs).toLocaleString()} ns/op ` +
    `(${Math.round(oldNs / newNs)}× 提升)`,
  );
  assert.ok(newNs <= oldNs, '当前实现不应比旧写法慢');
});

test('regression: sort key caching (A/B)', (t) => {
  const rows = makeRows(5000, 20);
  const kept = buildView(rows, COLUMNS, FILTERS, null); // 先过滤，两边同起点
  const expected = buildView(rows, COLUMNS, FILTERS, SORT);

  // 公平对照：两边都从"已过滤的行索引"出发，只比排序本身。
  // 旧排序：比较器里重算，必须复制数组（sort 原地）。
  // 新排序：buildView 接受 filters={} 跳过过滤，只走排序分支。
  const oldNs = timeIt(() => { sortOldWay(kept.slice(), rows); }, 30);
  const newNs = timeIt(() => { buildView(rows, COLUMNS, {}, SORT); }, 30);
  assert.deepEqual(sortOldWay(kept.slice(), rows), buildView(rows, COLUMNS, {}, SORT));

  t.diagnostic(
    `B 排序 key 缓存：旧(比较器重算) ${Math.round(oldNs).toLocaleString()} ns/op → 新(每行缓存) ${Math.round(newNs).toLocaleString()} ns/op ` +
    `(${Math.round(oldNs / newNs)}× 提升)`,
  );
});
