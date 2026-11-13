import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

// 可及性基线契约（S-04）在仓库上一级的工作区根：../.. = TableProGo，../../.. = db。
// 这里断言契约本身覆盖 web 端的三条规则——契约被删/被改窄时，前端的两条既有测试
// （accessible-names / contrast）就失去了依据，本用例负责先红。
const CONTRACT = JSON.parse(
  readFileSync(fileURLToPath(new URL('../../../dbcore/spec/a11y-baseline.json', import.meta.url)), 'utf8'),
) as {
  version: number;
  rules: { id: string; applies_to: string[]; min_ratio?: number }[];
};

function rule(id: string) {
  const r = CONTRACT.rules.find((x) => x.id === id);
  assert.ok(r, `契约缺少规则 ${id}`);
  return r!;
}

test('a11y 契约覆盖 web 端的可交互名字规则', () => {
  const r = rule('interactive-name');
  assert.ok(r.applies_to.includes('web'), 'interactive-name 必须适用于 web');
});

test('a11y 契约覆盖 web 端的对比度规则并给出阈值', () => {
  const r = rule('contrast');
  assert.ok(r.applies_to.includes('web'), 'contrast 必须适用于 web');
  assert.equal(typeof r.min_ratio, 'number', 'contrast 必须声明 min_ratio');
  assert.ok(r.min_ratio! >= 4.5, `min_ratio 不应低于 WCAG AA 小字号阈值，得到 ${r.min_ratio}`);
});

test('a11y 契约覆盖键盘可达与"不只靠颜色"', () => {
  assert.ok(rule('keyboard-reachable').applies_to.includes('web'));
  const colorOnly = rule('no-color-only-signal');
  assert.ok(colorOnly.applies_to.includes('web'));
  assert.ok(colorOnly.applies_to.includes('tui'), 'TUI 端更容易只靠颜色/位置表达状态');
});
