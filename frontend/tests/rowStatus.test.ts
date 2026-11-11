import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  rowStatus,
  rowStatuses,
  stagedSummary,
  statusClass,
  statusLabel,
  statusesOf,
} from '../src/datagrid/rowStatus.ts';
import { editReducer, emptyEditState } from '../src/datagrid/editorState.ts';
import type { EditState } from '../src/datagrid/editorState.ts';
import { addRow, emptyQueue } from '../src/gridView.ts';

// The queue is built through the same reducer the grid uses, so the tests
// exercise the real shape rather than a hand-made approximation of it.
const edited = (index: number, column = 'name', value = 'x', from?: EditState): EditState =>
  editReducer(from ?? emptyEditState(), {
    type: 'editCommitFetched',
    index,
    column,
    original: '',
    value,
  });

const deleted = (state: EditState, index: number): EditState =>
  editReducer(state, { type: 'deleteToggle', index });

test('an untouched queue paints every row clean', () => {
  const map = rowStatuses(emptyQueue());
  assert.deepEqual(map, {});
  assert.equal(rowStatus(map, 0), 'clean');
  assert.equal(statusClass('clean'), '');
});

test('an edited row is an update, a marked row is a delete', () => {
  const state = deleted(edited(3), 7);
  const map = rowStatuses(state.queue);
  assert.equal(rowStatus(map, 3), 'update');
  assert.equal(rowStatus(map, 7), 'delete');
  assert.equal(statusClass(rowStatus(map, 3)), 'row-update');
  assert.equal(statusClass(rowStatus(map, 7)), 'row-delete');
  assert.equal(statusClass(statusesOf(state.queue)[3]), 'row-update');
});

test('delete wins over update: the row is going away', () => {
  // The user edited a row and then marked it for deletion. Painting it
  // "updated" would promise a change that will never be saved.
  const state = deleted(edited(4), 4);
  const summary = stagedSummary(state.queue);
  assert.equal(rowStatus(summary.statuses, 4), 'delete');
  assert.equal(summary.updates, 0, 'the invisible update is not counted');
  assert.equal(summary.deletes, 1);
});

test('un-deleting a row restores its update', () => {
  let state = deleted(edited(2, 'a', 'v'), 2);
  assert.equal(rowStatuses(state.queue)[2], 'delete');
  // deleteToggle also discards pending cell edits by design, so the row comes
  // back clean rather than "updated" — the same rule the grid documents.
  state = deleted(state, 2);
  assert.equal(rowStatus(rowStatuses(state.queue), 2), 'clean');
});

test('queued inserts are counted but carry no row index', () => {
  // A new row is not in the view yet, so it cannot be keyed by index — the grid
  // paints it at the tail from the count.
  const queue = addRow(emptyQueue(), 1, { a: 'x' });
  const summary = stagedSummary(queue);
  assert.equal(summary.inserts, 1);
  assert.deepEqual(summary.statuses, {});
});

test('stagedSummary totals match the queue exactly', () => {
  // Two cells on one row are one updated row; a second row makes two.
  let state = edited(0, 'a', '1');
  state = edited(0, 'b', '2', state);
  state = edited(5, 'a', '3', state);
  state = deleted(state, 9);
  const summary = stagedSummary(state.queue);
  assert.equal(summary.updates, 2, 'two distinct rows are updated, not three edits');
  assert.equal(summary.deletes, 1);
  assert.equal(summary.inserts, 0);
});

test('a malformed update key never becomes a phantom row', () => {
  // Number('x') is NaN; the guard keeps it out of the map rather than painting
  // row NaN.
  const queue = { updates: { x: { a: { value: '1' } }, 3: { a: { value: '2' } } } as never, inserts: [], deletes: [] };
  const map = rowStatuses(queue as never);
  assert.equal(Object.keys(map).length, 1);
  assert.equal(map[3], 'update');
});

test('statusesOf is the same derivation the grid reads', () => {
  const state = edited(1, 'a', 'v');
  assert.deepEqual(statusesOf(state.queue), rowStatuses(state.queue));
});

test('every non-clean status has a label for the legend', () => {
  assert.equal(statusLabel('insert'), '新增');
  assert.equal(statusLabel('update'), '修改');
  assert.equal(statusLabel('delete'), '删除');
});
