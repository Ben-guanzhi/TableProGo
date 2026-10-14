import assert from 'node:assert/strict';
import { test } from 'node:test';

import { emptyFKState, fkReducer, highlightedIndex, pickerList } from '../src/datagrid/fkState.ts';
import type { FKAction, FKState } from '../src/datagrid/fkState.ts';
import type { ForeignKeyCandidates } from '../src/api.ts';

const run = (actions: FKAction[]): FKState =>
  actions.reduce(fkReducer, emptyFKState());

const pickerAt = (rowKey = 'u0', column = 'user_id'): FKState['picker'] => ({
  rowKey,
  column,
  left: 10,
  top: 40,
  width: 200,
  error: '',
});

const list = (over: Partial<ForeignKeyCandidates> = {}): ForeignKeyCandidates => ({
  refTable: 'users',
  refColumn: 'id',
  labelColumn: 'name',
  values: [
    { value: '1', label: 'Ada' },
    { value: '2', label: 'Bob' },
    { value: '3', label: 'Cathy' },
  ],
  truncated: false,
  ...over,
});

test('a fresh grid has no open picker', () => {
  const state = emptyFKState();
  assert.equal(state.picker, null);
  assert.equal(state.query, '');
  assert.equal(state.activeOption, -1);
  assert.deepEqual(state.lists, {});
  assert.deepEqual(state.foreignKeys, []);
});

// The two things a stale highlight would corrupt are the query and the
// highlight: reopening a cell must start from the full list, not from whatever
// the previous cell had narrowed to.
test('opening a picker resets the query and the highlight', () => {
  const dirty = run([
    { type: 'pickerOpen', picker: pickerAt('u0') },
    { type: 'queryType', value: 'bo' },
    { type: 'activeMove', active: 2 },
  ]);
  assert.equal(dirty.query, 'bo');
  assert.equal(dirty.activeOption, 2);

  const reopened = fkReducer(dirty, { type: 'pickerOpen', picker: pickerAt('u1') });
  assert.equal(reopened.query, '');
  assert.equal(reopened.activeOption, -1);
});

test('closing an already-closed picker changes nothing', () => {
  const state = emptyFKState();
  assert.equal(fkReducer(state, { type: 'pickerClose' }), state);
});

// A slow response must not annotate the cell that opened after it was sent.
// The guard is on the cell identity, not on timing.
test('a stale read error lands only on the cell that asked', () => {
  const opened = run([
    { type: 'pickerOpen', picker: pickerAt('u0', 'user_id') },
  ]);

  const foreign = fkReducer(opened, {
    type: 'pickerError',
    rowKey: 'u5',
    column: 'user_id',
    error: 'offline',
  });
  assert.equal(foreign, opened, 'a foreign cell leaves the state untouched');

  const own = fkReducer(opened, { type: 'pickerError', rowKey: 'u0', column: 'user_id', error: 'offline' });
  assert.equal(own.picker?.error, 'offline');
});

// The highlight returning to the top is what makes the list predictable: the
// typed text always selects the first row that still matches it.
test('typing narrows the list and resets the highlight to the top', () => {
  const state = run([
    { type: 'pickerOpen', picker: pickerAt('u0', 'user_id') },
    { type: 'activeMove', active: 2 },
    { type: 'queryType', value: 'a' },
  ]);
  assert.equal(state.query, 'a');
  assert.equal(state.activeOption, 0);
});

test('the first read for a column wins', () => {
  const first = run([{ type: 'listSet', key: 'user_id', value: list({ refTable: 'users' }) }]);
  assert.equal(first.lists['user_id']?.refTable, 'users');

  const second = fkReducer(first, { type: 'listSet', key: 'user_id', value: list({ refTable: 'other' }) });
  assert.equal(second, first, 'a late response cannot replace what is on screen');
  assert.equal(second.lists['user_id']?.refTable, 'users');
});

test('a new source clears the cache and the open picker', () => {
  const dirty = run([
    { type: 'pickerOpen', picker: pickerAt('u0') },
    { type: 'listSet', key: 'user_id', value: list() },
    { type: 'constraintsSet', foreignKeys: [{ name: 'fk', columns: ['user_id'] }] as any },
  ]);
  assert.notDeepEqual(dirty, emptyFKState());
  assert.deepEqual(fkReducer(dirty, { type: 'fkReset' }), emptyFKState());
});

// The list is the join of the cache and the query, so a cached column that is
// narrowed still narrows, and one that is not cached is empty.
test('the list is the cached candidates narrowed by the query', () => {
  const open = run([{ type: 'pickerOpen', picker: pickerAt('u0', 'user_id') }]);
  assert.deepEqual(pickerList(open), [], 'nothing cached yet');

  const cached = run([
    { type: 'pickerOpen', picker: pickerAt('u0', 'user_id') },
    { type: 'listSet', key: 'user_id', value: list() },
  ]);
  assert.equal(pickerList(cached).length, 3, 'an empty query shows everything');

  const narrowed = fkReducer(cached, { type: 'queryType', value: 'a' });
  assert.deepEqual(pickerList(narrowed).map(c => c.value), ['1', '3']);
});

// The cache is keyed by the lowercased column name, so a mixed-case column
// reaches it.
test('the cache is case-insensitive', () => {
  const cached = run([
    { type: 'pickerOpen', picker: pickerAt('u0', 'user_Id') },
    { type: 'listSet', key: 'user_id', value: list() },
  ]);
  assert.equal(pickerList(cached).length, 3);
});

// Narrowing can shrink the list under the highlight, so the index must be
// clamped - otherwise Enter would take a value that is no longer visible.
test('the highlight is clamped to the narrowed list', () => {
  assert.equal(highlightedIndex(2, 3), 2);
  assert.equal(highlightedIndex(2, 1), 0);
  assert.equal(highlightedIndex(0, 0), -1);
  assert.equal(highlightedIndex(-1, 3), 0);
});

test('unknown actions leave the state alone', () => {
  const state = emptyFKState();
  assert.equal(fkReducer(state, { type: 'nope' } as any), state);
});
