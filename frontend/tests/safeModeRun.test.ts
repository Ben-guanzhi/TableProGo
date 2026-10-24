import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  cancelledResult,
  confirmMessage,
  confirmThenRun,
  emptyResult,
} from '../src/lib/safeModeRun.ts';

const base = {
  columns: [],
  rows: [],
  rowCount: 0,
  affectedRows: 0,
  lastInsertId: 0,
  executionTime: 0,
};

test('a result that was not paused passes straight through', async () => {
  let resigned = false;
  const result = await confirmThenRun(
    { ...base, rowCount: 7 },
    async () => {
      resigned = true;
      return base;
    },
    () => true,
  );
  assert.equal(result.rowCount, 7);
  assert.equal(resigned, false, 'nothing should be re-sent without a pause');
});

test('accepting the prompt re-sends through the caller', async () => {
  let resigned = false;
  const result = await confirmThenRun(
    { ...base, error: 'Safe Mode: needs confirmation', needsConfirmation: true },
    async () => {
      resigned = true;
      return { ...base, affectedRows: 3 };
    },
    message => {
      assert.match(message, /Safe Mode: needs confirmation/);
      return true;
    },
  );
  assert.equal(resigned, true);
  assert.equal(result.affectedRows, 3);
});

test('declining cancels and never re-sends', async () => {
  let resigned = false;
  const result = await confirmThenRun(
    { ...base, error: 'Safe Mode: blocked', needsConfirmation: true },
    async () => {
      resigned = true;
      return base;
    },
    () => false,
  );
  assert.equal(resigned, false, 'a declined statement must not run');
  assert.equal(result.error, 'Cancelled (Safe Mode)');
  assert.equal(result.needsConfirmation, false);
});

test('the prompt text carries the backend reason', () => {
  const message = confirmMessage({ ...base, error: 'Safe Mode: a DELETE without a WHERE clause will rewrite every row' });
  assert.match(message, /rewrite every row/);
  assert.match(message, /Run the statement anyway\?/);
});

test('cancelledResult is an empty result with the cancellation error', () => {
  const result = cancelledResult();
  assert.equal(result.error, 'Cancelled (Safe Mode)');
  assert.equal(result.rowCount, 0);
  assert.deepEqual(result.columns, []);
});

test('emptyResult has every field the grid reads', () => {
  const result = emptyResult();
  assert.deepEqual(result, {
    columns: [],
    rows: [],
    rowCount: 0,
    affectedRows: 0,
    lastInsertId: 0,
    executionTime: 0,
  });
});
