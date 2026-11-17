// CSV 公式注入防护（OWASP CSV Injection）：以 = + - @ 开头的单元格写进 CSV，
// 用户用 Excel 打开会当公式执行。必须前缀单引号；纯数字字面量豁免。

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { toCSV, formulaGuard } from '../src/gridView.ts';

const cols = [{ name: 'a' }, { name: 'b' }];

test('formulaGuard 转义危险前缀，放过纯数字', () => {
  assert.equal(formulaGuard("=cmd|' /C calc'!A0"), "'=cmd|' /C calc'!A0");
  assert.equal(formulaGuard('+cmd'), "'+cmd");
  assert.equal(formulaGuard('-cmd'), "'-cmd");
  assert.equal(formulaGuard('@SUM(A1)'), "'@SUM(A1)");
  // 纯数字不动——否则破坏 Excel 里的数值计算。
  assert.equal(formulaGuard('-5'), '-5');
  assert.equal(formulaGuard('+3.14'), '+3.14');
  assert.equal(formulaGuard('1e5'), '1e5');
  assert.equal(formulaGuard('hello'), 'hello');
  assert.equal(formulaGuard(''), '');
});

test('toCSV 对公式注入做转义', () => {
  const out = toCSV(cols, [['=1+1', '-5']]);
  assert.ok(out.includes("'=1+1"), `危险公式没有被转义:\n${out}`);
  assert.ok(out.includes('-5'), '纯数字丢失');
  assert.ok(!out.includes("'-5"), '纯数字被误转义了');
});
