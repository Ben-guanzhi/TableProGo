import assert from 'node:assert/strict';
import { test } from 'node:test';

// The assistant's client module talks to the backend over the loopback API. The
// parts worth pinning without a browser are the request shapes: the backend
// refuses a body it does not recognise, so a renamed field is a broken feature.

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

const { AskLlm, ClearLlmHistory, LoadLlmConfig, LoadLlmHistory, SaveLlmConfig, defaultLlmConfig } = await import(
  '../src/lib/llm.ts'
);

test('the question goes in a question field', async () => {
  calls.length = 0;
  nextResponse = { answer: 'there are 3 tables' };
  const answer = await AskLlm('what is there?');
  assert.equal(answer, 'there are 3 tables');
  assert.equal(calls.length, 1);
  assert.match(calls[0].path, /\/api\/llm\/ask$/);
  assert.deepEqual(calls[0].body, { question: 'what is there?' });
});

test('the config round trip keeps the provider fields', async () => {
  calls.length = 0;
  nextResponse = {
    config: { provider: 'openai', baseURL: 'http://x', apiKey: 'k', model: 'm', enabled: true },
    providers: [{ ID: 'openai', Label: 'OpenAI', RequiresKey: true, DefaultBaseURL: '', DefaultModel: '' }],
  };
  const res = await LoadLlmConfig();
  assert.equal(res.config.provider, 'openai');
  assert.equal(res.config.enabled, true);
  assert.equal(res.providers.length, 1);
  assert.equal(res.providers[0].Label, 'OpenAI');
});

test('a missing config falls back to the defaults', async () => {
  nextResponse = {};
  const res = await LoadLlmConfig();
  assert.equal(res.config.provider, defaultLlmConfig.provider);
  assert.equal(res.config.enabled, false);
  assert.deepEqual(res.providers, []);
});

test('saving sends the config and the save flag', async () => {
  calls.length = 0;
  nextResponse = {};
  await SaveLlmConfig({ provider: 'ollama', baseURL: 'http://local', apiKey: '', model: 'llama3', enabled: true });
  assert.match(calls[0].path, /\/api\/llm\/config$/);
  const body = calls[0].body as { config: { provider: string }; save: boolean };
  assert.equal(body.save, true);
  assert.equal(body.config.provider, 'ollama');
});

test('the history is read and cleared', async () => {
  calls.length = 0;
  nextResponse = { history: [{ role: 'user', content: 'hi' }] };
  const history = await LoadLlmHistory();
  assert.equal(history.length, 1);
  assert.equal(history[0].content, 'hi');

  calls.length = 0;
  nextResponse = { cleared: true };
  await ClearLlmHistory();
  assert.match(calls[0].path, /\/api\/llm\/clear$/);
});
