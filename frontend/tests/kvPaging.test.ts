import assert from 'node:assert/strict';
import { test } from 'node:test';

import { mergeKVWindow } from '../src/kvPaging.ts';
import type { KVValue } from '../src/types/index.ts';

const window = (rows: any[][], next = '', done = false): KVValue => ({
  key: 'biglist',
  type: 'list',
  ttl: '永不过期',
  next,
  done,
  result: {
    columns: [
      { name: 'index', type: 'BIGINT', nullable: false },
      { name: 'value', type: 'TEXT', nullable: true },
    ],
    rows,
    rowCount: rows.length,
    affectedRows: 0,
    lastInsertId: 0,
    executionTime: 0,
    truncated: !done,
  },
});

test('a continuation appends its rows and re-reads the server verdict', () => {
  const first = window([[0, 'a'], [1, 'b']], '2');
  const second = window([[2, 'c']], '', true);
  const merged = mergeKVWindow(first, second);
  assert.deepEqual(merged.result.rows, [[0, 'a'], [1, 'b'], [2, 'c']]);
  assert.equal(merged.result.rowCount, 3);
  // The banner follows the newest window: it says done, so nothing is hidden.
  assert.equal(merged.result.truncated, false);
  assert.equal(merged.done, true);
  assert.equal(merged.next, '');
});

test('two continuations chain without losing the middle window', () => {
  const first = window([[0, 'a']], '1');
  const second = window([[1, 'b']], '2');
  const third = window([[2, 'c']], '', true);
  const merged = mergeKVWindow(mergeKVWindow(first, second), third);
  assert.deepEqual(merged.result.rows, [[0, 'a'], [1, 'b'], [2, 'c']]);
});

test('identity columns survive the append (list index is the real rank)', () => {
  // The backend numbers a continuation by the item's rank, so the grid's
  // LSET target stays correct after the merge; a zero-based renumber would
  // have pointed the edit at the first item.
  const first = window([[0, 'a']], '1000');
  const second = window([[1000, 'edited']], '', true);
  const merged = mergeKVWindow(first, second);
  assert.equal(merged.result.rows[1][0], 1000);
});
