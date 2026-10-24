import assert from 'node:assert/strict';
import { test } from 'node:test';

// The proposal routes are the whole of the assistant's write path: the model
// proposes, the user disposes. The request shapes are pinned here so a renamed
// field cannot silently make "执行" stop working.

const calls: { path: string; body: unknown }[] = [];
let nextResponse: unknown = {};

(globalThis as any).window = {
  __API_PORT__: '1',
  __API_TOKEN__: 't',
};
(globalThis as any).fetch = async (url: string, init: { body?: string }) => {
  calls.push({ path: url, body: init.body ? JSON.parse(init.body) : undefined });
  return {
    ok: true,
    status: 200,
    json: async () => nextResponse,
  };
};

const { AcceptLlmProposal, LoadLlmProposals, RejectLlmProposal } = await import('../src/lib/llm.ts');

test('the waiting proposals are read', async () => {
  calls.length = 0;
  nextResponse = {
    proposals: [
      { id: 'prop-1', connection: 'c1', database: 'app', sql: 'DELETE FROM t', reason: 'because' },
    ],
  };
  const proposals = await LoadLlmProposals();
  assert.equal(proposals.length, 1);
  assert.equal(proposals[0].id, 'prop-1');
  assert.equal(proposals[0].sql, 'DELETE FROM t');
  assert.equal(proposals[0].reason, 'because');
  assert.match(calls[0].path, /\/api\/llm\/proposals$/);
});

test('accepting sends the id and reports the result', async () => {
  calls.length = 0;
  nextResponse = { result: { RowCount: 3, Error: '' } };
  const result = await AcceptLlmProposal('prop-1');
  assert.equal(calls.length, 1);
  assert.match(calls[0].path, /\/api\/llm\/proposal\/accept$/);
  assert.deepEqual(calls[0].body, { id: 'prop-1' });
  assert.equal(result.rowCount, 3);
  assert.equal(result.error, undefined);
});

test('a refused statement comes back as an error the panel can show', async () => {
  nextResponse = { result: { Error: 'Safe Mode blocked this statement' } };
  const result = await AcceptLlmProposal('prop-2');
  assert.match(result.error ?? '', /Safe Mode/);
});

test('rejecting sends the id and does not run anything', async () => {
  calls.length = 0;
  nextResponse = { rejected: true };
  await RejectLlmProposal('prop-3');
  assert.match(calls[0].path, /\/api\/llm\/proposal\/reject$/);
  assert.deepEqual(calls[0].body, { id: 'prop-3' });
});
