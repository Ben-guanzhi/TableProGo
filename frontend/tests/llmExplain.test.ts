import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

// SQLucid 三件套的 API 契约（Q-01/Q-02）：refine 与 explain 两个新路由的请求/
// 响应形状在这里钉住——llm.ts 的包装是 assistant 的写路径之外仅有的两个 AI 面。

const api = readFileSync(fileURLToPath(new URL('../src/lib/llm.ts', import.meta.url)), 'utf8');

test('refine 路由已接线（POST /api/llm/proposal/refine）', async () => {
  assert.ok(api.includes('/api/llm/proposal/refine'), 'llm.ts 缺 refine 包装');
  assert.ok(/RefineLlmProposal/.test(api), 'llm.ts 缺 RefineLlmProposal 导出');
  // 请求体必须带 id 与 feedback（修复环的输入）。
  assert.ok(/feedback/.test(api), 'refine 请求应携带 feedback');
});

test('explain 路由已接线（POST /api/llm/proposal/explain）', async () => {
  assert.ok(api.includes('/api/llm/proposal/explain'), 'llm.ts 缺 explain 包装');
  assert.ok(/ExplainLlmSQL|LlmSqlStep/.test(api), 'llm.ts 缺 explain 的类型导出');
});

test('LlmSqlStep 形状：clause/text/columns（hover 高亮的输入）', () => {
  // llm.ts 里的接口定义逐字检查，防止字段被改名而 AiPanel 的 hover 高亮断链。
  const src = readFileSync(fileURLToPath(new URL('../src/lib/llm.ts', import.meta.url)), 'utf8');
  for (const field of ['clause', 'text', 'columns']) {
    assert.ok(new RegExp(`${field}:`).test(src), `LlmSqlStep 缺字段 ${field}`);
  }
});
