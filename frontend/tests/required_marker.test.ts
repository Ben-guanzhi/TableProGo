// 源码不变量：带 `required` 的输入，其标签要说出「必填」。
//
// 与 dbstudio 的 ui_invariants_test.go 同一个方法：**配置会被绕过，源码不会**。
// 做法是扫描源码而不是渲染——标签与 input 的对应关系由 id/htmlFor 建立。

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, '..');

function read(p: string): string {
  return readFileSync(join(root, p), 'utf8');
}

test('带 required 的输入，其 <label> 带 * 标记', () => {
  const src = read('src/components/ConnectionDialog.tsx');

  // 每个 <label htmlFor="X">…</label> 与其后的、id="X" 的控件配对。
  const labelRe = /<label htmlFor="([^"]+)"[^>]*>([\s\S]*?)<\/label>/g;
  const requiredIds: string[] = [];
  for (const m of src.matchAll(labelRe)) {
    const [, id, text] = m;
    // 控件在 label 之后；取 label 到下一个 label 之间的片段找 id + required。
    const tail = src.slice(m.index! + m[0].length, m.index! + m[0].length + 900);
    const ctrl = tail.match(new RegExp(`id="${id}"[\\s\\S]{0,400}?required`));
    if (ctrl) {
      requiredIds.push(id);
      assert.ok(
        text.includes('*'),
        `label for="${id}" 标记了 required，但标签文案里没有 *`,
      );
    }
  }
  assert.ok(requiredIds.length > 0, '没找到带 required 的输入——正则或结构变了？');
});
