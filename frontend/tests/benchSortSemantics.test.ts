import { performance } from 'node:perf_hooks';
import { test } from 'node:test';
import assert from 'node:assert/strict';

// §320：localeCompare 的语言规则 vs < 的码点序——语义是否等价？
// 数据库结果的文本排序传统上是二进制/码点序（ORDER BY col 在多数引擎
// 默认二进制排序），不是 locale-aware；但 gridView 当前用 localeCompare。
// 本基准列出两者不一致的真实样本，让"换 <"的决定基于证据。

const samples = [
  'a', 'B', 'ä', 'A', 'b', 'z', 'Z', '10', '2',
  'Row_1', 'row_1', 'ROW_1', 'naïve', 'navy', 'é', 'e',
  '中文', ' convolutional', '你好', 'ábc', 'abc',
];

test('audit: localeCompare vs codepoint order disagreements', (t) => {
  const disagreements: string[] = [];
  for (let i = 0; i < samples.length; i++) {
    for (let j = i + 1; j < samples.length; j++) {
      const a = samples[i], b = samples[j];
      const loc = a.localeCompare(b);
      const cp = a < b ? -1 : a > b ? 1 : 0;
      if (loc !== cp) disagreements.push(`${JSON.stringify(a)} vs ${JSON.stringify(b)}: locale=${loc} codepoint=${cp}`);
    }
  }
  t.diagnostic(`disagreements: ${disagreements.length} / ${(samples.length * (samples.length - 1)) / 2}`);
  for (const d of disagreements.slice(0, 12)) t.diagnostic(d);
  // 不 assert 为零——目的是记录真实差异，由人决定语义。
  assert.ok(true);
});
