import assert from 'node:assert/strict';
import { test } from 'node:test';

import { nextFocusable } from '../src/dialog.ts';

const a = { id: 'a' } as unknown as HTMLElement;
const b = { id: 'b' } as unknown as HTMLElement;
const c = { id: 'c' } as unknown as HTMLElement;
const list = [a, b, c];

test('Tab walks the dialog controls and wraps at the end', () => {
  assert.equal(nextFocusable(list, a, false), b);
  assert.equal(nextFocusable(list, b, false), c);
  assert.equal(nextFocusable(list, c, false), a);
});

test('Shift+Tab walks backwards and wraps at the start', () => {
  assert.equal(nextFocusable(list, c, true), b);
  assert.equal(nextFocusable(list, a, true), c);
});

test('focus entering from outside the dialog starts at an end', () => {
  assert.equal(nextFocusable(list, null, false), a);
  assert.equal(nextFocusable(list, { id: 'outside' } as unknown as HTMLElement, true), c);
});

test('an empty dialog has nowhere to go', () => {
  assert.equal(nextFocusable([], null, false), null);
});
