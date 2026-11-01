import assert from 'node:assert/strict';
import { test } from 'node:test';

import { txBusy, txGate, txSettle, type TxOp } from '../src/lib/txButtons.ts';

// A click is a call to txGate; a settled request is txSettle. The tab's pending
// op is the only state, exactly as App.tsx keeps it.

test('a first click records its op as pending', () => {
  assert.equal(txGate(null, 'begin'), 'begin');
  assert.equal(txGate(null, 'commit'), 'commit');
  assert.equal(txGate(null, 'rollback'), 'rollback');
});

test('a second click while an op is in flight is ignored', () => {
  const pending = txGate(null, 'begin');
  assert.equal(txGate(pending, 'begin'), 'begin'); // double-click on begin
  assert.equal(txGate(pending, 'commit'), 'begin'); // commit during begin
  assert.equal(txGate(pending, 'rollback'), 'begin'); // rollback during begin
});

test('settle clears the pending op so the next click runs', () => {
  const pending = txGate(null, 'commit');
  assert.equal(txSettle(pending), null);
  assert.equal(txGate(null, 'begin'), 'begin');
});

test('the buttons are disabled exactly while an op is pending', () => {
  assert.equal(txBusy(null), false);
  const pending: TxOp = 'begin';
  assert.equal(txBusy(pending), true);
  assert.equal(txBusy(txSettle(pending)), false);
});
