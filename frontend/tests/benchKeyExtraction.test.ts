import { performance } from 'node:perf_hooks';
import { test } from 'node:test';
import assert from 'node:assert/strict';

// §320：定位 B 项（排序 key）真实瓶颈的微基准。
// 假设有三个：
//   H1 DECIMAL_SHAPE.test 正则成本高 → 但 cellKey 只在 numeric=true 时调用它
//   H2 每行一个对象分配是瓶颈 → §319 的 SoA 已证伪
//   H3 comparator 里的 localeCompare 比 < 慢很多 → 本基准验证
// 先量化再改（§315 纪律）。

const SHAPE = /^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$/;

const strings = Array.from({ length: 5000 }, (_, i) => `row${String(i).padStart(6, '0')}`);

function timeIt(fn: () => void, iters: number): number {
  const start = performance.now();
  for (let i = 0; i < iters; i++) fn();
  return ((performance.now() - start) / iters) * 1e6;
}

test('bench: localeCompare vs < on 100k comparisons', (t) => {
  let sink = 0;
  const nsLocale = timeIt(() => {
    for (let i = 1; i < strings.length; i++) sink += strings[i - 1].localeCompare(strings[i]);
  }, 30);
  const nsCmp = timeIt(() => {
    for (let i = 1; i < strings.length; i++) sink += strings[i - 1] < strings[i] ? -1 : 1;
  }, 30);
  assert.ok(Number.isFinite(sink));
  t.diagnostic(
    `H3 localeCompare ${Math.round(nsLocale).toLocaleString()} ns/op vs < ${Math.round(nsCmp).toLocaleString()} ns/op ` +
    `(${Math.round(nsLocale / nsCmp)}× 差距)`,
  );
});

test('bench: DECIMAL_SHAPE.test numeric vs plain path', (t) => {
  const nums = strings.map((_, i) => String(i));
  let sink = 0;
  const nsRegex = timeIt(() => { for (const s of nums) if (SHAPE.test(s)) sink++; }, 100);
  const nsTrim = timeIt(() => { for (const s of nums) if (s.trim()) sink++; }, 100);
  assert.ok(sink > 0);
  t.diagnostic(
    `H1 DECIMAL_SHAPE.test ${Math.round(nsRegex).toLocaleString()} ns/op vs trim ${Math.round(nsTrim).toLocaleString()} ns/op ` +
    `(${Math.round(nsRegex / nsTrim)}× 差距)`,
  );
});

test('bench: cellText vs object allocation on 5000 strings', (t) => {
  let sink = 0;
  const nsText = timeIt(() => { for (const s of strings) sink += s.length; }, 100);
  const nsObj = timeIt(() => {
    for (const s of strings) { const o = { isNull: false, num: null, decText: null, text: s }; sink += o.text.length; }
  }, 100);
  assert.ok(sink > 0);
  t.diagnostic(
    `H2 text only ${Math.round(nsText).toLocaleString()} ns/op vs text+object ${Math.round(nsObj).toLocaleString()} ns/op ` +
    `(${Math.round(nsObj / nsText)}× 差距)`,
  );
});
