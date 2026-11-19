import { performance } from 'node:perf_hooks';
import { test } from 'node:test';
import assert from 'node:assert/strict';

// §320：localeCompare 慢是因为每次调用重新解析 locale；复用一个 Intl.Collator
// 实例的 compare 应当保语义且快。先量化 + 语义验证。

const a = Array.from({ length: 5000 }, (_, i) => `Row_${String(i).padStart(6, '0')}`);
const b = Array.from({ length: 5000 }, (_, i) => `row_${String(i + 1).padStart(6, '0')}`);
const collator = new Intl.Collator(undefined, { numeric: false, sensitivity: 'variant' });

function timeIt(fn: () => void, iters: number): number {
  const start = performance.now();
  for (let i = 0; i < iters; i++) fn();
  return ((performance.now() - start) / iters) * 1e6;
}

test('bench: localeCompare vs Intl.Collator.compare', (t) => {
  // 语义一致性：同一 collator 实例与 String.localeCompare 默认行为一致。
  for (let i = 0; i < a.length; i += 101) {
    assert.equal(collator.compare(a[i], b[i]), a[i].localeCompare(b[i]));
  }
  let sink = 0;
  const nsLocale = timeIt(() => {
    for (let i = 0; i < a.length; i++) sink += a[i].localeCompare(b[i]);
  }, 30);
  const nsCollator = timeIt(() => {
    for (let i = 0; i < a.length; i++) sink += collator.compare(a[i], b[i]);
  }, 30);
  assert.ok(Number.isFinite(sink));
  t.diagnostic(
    `String.localeCompare ${Math.round(nsLocale).toLocaleString()} ns/op vs Intl.Collator.compare ${Math.round(nsCollator).toLocaleString()} ns/op ` +
    `(${Math.round(nsLocale / nsCollator)}× 提升)`,
  );
  assert.ok(nsCollator <= nsLocale, '复用的 Collator 不应更慢');
});
