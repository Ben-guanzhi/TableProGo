import assert from 'node:assert/strict';
import { test } from 'node:test';

import { boundaryDetail } from '../src/lib/boundaryText.ts';

test('an Error instance renders its message', () => {
  assert.equal(boundaryDetail(new Error('boom')), 'boom');
});

test('any other thrown value renders as text', () => {
  assert.equal(boundaryDetail('plain string'), 'plain string');
  assert.equal(boundaryDetail(42), '42');
  assert.equal(boundaryDetail({ code: 1 }), '[object Object]');
});

// The boundary is the last line of defence: a value whose String() throws must
// still produce a line instead of crashing the fallback itself.
test('an unprintable error still renders a line', () => {
  const hostile = {
    toString() {
      throw new Error('nope');
    },
  };
  assert.equal(boundaryDetail(hostile), 'unprintable error');
});
