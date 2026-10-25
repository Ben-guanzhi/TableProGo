import assert from 'node:assert/strict';
import { test } from 'node:test';

import { emptyQueue } from '../src/gridView.ts';
import type { ChangeQueue } from '../src/gridView.ts';
import { editReducer, emptyEditState } from '../src/datagrid/editorState.ts';
import type { EditAction, EditState } from '../src/datagrid/editorState.ts';

// Apply a run of actions, the way a user gesture would.
const run = (actions: EditAction[]): EditState =>
  actions.reduce(editReducer, emptyEditState());

const withQueue = (queue: ChangeQueue): EditState => ({ ...emptyEditState(), queue });

test('a fresh grid has nothing queued and nothing open', () => {
  const state = emptyEditState();
  assert.equal(state.selected, null);
  assert.equal(state.editing, null);
  assert.equal(state.draft, '');
  assert.equal(state.review, false);
  assert.equal(state.applying, false);
  assert.deepEqual(state.queue, emptyQueue());
});

test('beginning an edit opens the editor and seeds the draft', () => {
  const state = run([
    { type: 'editBegin', key: 'u3', column: 'name', value: 'old' },
  ]);
  assert.deepEqual(state.editing, { key: 'u3', column: 'name' });
  assert.equal(state.draft, 'old');
});

test('typing keeps the queue untouched', () => {
  const before = withQueue(emptyQueue());
  const after = editReducer(before, { type: 'editDraft', value: 'new' });
  assert.equal(after.draft, 'new');
  assert.deepEqual(after.queue, before.queue);
});

test('a committed edit closes the editor and lands in the queue', () => {
  const state = run([
    { type: 'editBegin', key: 'u3', column: 'name', value: 'old' },
    { type: 'editCommitFetched', index: 3, column: 'name', value: 'new', original: 'old' },
  ]);
  assert.equal(state.editing, null, 'the editor must close with the commit');
  assert.deepEqual(state.queue.updates[3], { name: 'new' });
});

// Committing back on the original value is not an edit, so nothing is queued -
// otherwise the pending badge would lie about an unchanged row.
test('committing the original value queues nothing', () => {
  const state = run([
    { type: 'editBegin', key: 'u3', column: 'name', value: 'old' },
    { type: 'editCommitFetched', index: 3, column: 'name', value: 'old', original: 'old' },
  ]);
  assert.deepEqual(state.queue.updates, {});
  assert.equal(state.editing, null);
});

test('a new row is appended and its first column opens for typing', () => {
  const state = run([
    { type: 'rowAppend', id: 1, column: 'name' },
  ]);
  assert.equal(state.queue.inserts.length, 1);
  assert.deepEqual(state.queue.inserts[0], { id: 1, values: {} });
  assert.deepEqual(state.editing, { key: 'n1', column: 'name' });
  assert.equal(state.draft, '');
});

// Typing in one queued row must not renumber another, so the id is carried, not
// derived from the position.
test('typing into a queued row targets it by id', () => {
  const state = run([
    { type: 'rowAppend', id: 1, column: 'name' },
    { type: 'rowAppend', id: 2, column: 'name' },
    { type: 'editBegin', key: 'n1', column: 'name', value: '' },
    { type: 'editCommitFresh', id: 1, column: 'name', value: 'first' },
  ]);
  assert.deepEqual(state.queue.inserts, [
    { id: 1, values: { name: 'first' } },
    { id: 2, values: {} },
  ]);
  assert.equal(state.editing, null);
});

// An empty cell in a new row says nothing about that column, which is how a
// column keeps its database default instead of being written as "".
test('an empty cell in a queued row is left unwritten', () => {
  const state = run([
    { type: 'rowAppend', id: 1, column: 'name' },
    { type: 'editBegin', key: 'n1', column: 'note', value: 'x' },
    { type: 'editCommitFresh', id: 1, column: 'note', value: '' },
  ]);
  assert.deepEqual(state.queue.inserts[0].values, {});
});

test('a duplicate carries the values it was given', () => {
  const state = run([
    { type: 'rowDuplicate', id: 7, values: { name: 'copy', note: null as any } },
  ]);
  assert.deepEqual(state.queue.inserts, [{ id: 7, values: { name: 'copy', note: null } }]);
});

// Marking a row for deletion drops its pending edits: a row that is going away
// has nothing left to update. Marking it again is the undo.
test('delete toggles and discards the pending edits on the way out', () => {
  const edited = run([
    { type: 'editBegin', key: 'u2', column: 'name', value: 'old' },
    { type: 'editCommitFetched', index: 2, column: 'name', value: 'new', original: 'old' },
  ]);
  const marked = editReducer(edited, { type: 'deleteToggle', index: 2 });
  assert.deepEqual(marked.queue.deletes, [2]);
  assert.deepEqual(marked.queue.updates, {});
  assert.equal(marked.editing, null, 'the open editor closes with the row');

  const unmarked = editReducer(marked, { type: 'deleteToggle', index: 2 });
  assert.deepEqual(unmarked.queue.deletes, []);
});

test('discarding the queue empties it but leaves the selection', () => {
  const edited = run([
    { type: 'editSelect', index: 4 },
    { type: 'editBegin', key: 'u4', column: 'name', value: 'old' },
    { type: 'editCommitFetched', index: 4, column: 'name', value: 'new', original: 'old' },
  ]);
  const cleared = editReducer(edited, { type: 'queueClear' });
  assert.deepEqual(cleared.queue, emptyQueue());
  assert.equal(cleared.selected, 4);
});

test('applying clears the queue, the review and the preview text', () => {
  const pending = run([
    { type: 'reviewOpen' },
    { type: 'preview', statements: ['UPDATE t SET name = 1'], error: '' },
  ]);
  assert.deepEqual(pending.statements, ['UPDATE t SET name = 1']);

  const applying = editReducer(pending, { type: 'applyStart' });
  assert.equal(applying.applying, true);
  assert.equal(applying.applyError, '');

  const applied = editReducer(applying, { type: 'applyResult', ok: true, error: '' });
  assert.equal(applied.applying, false);
  assert.equal(applied.review, false);
  assert.deepEqual(applied.queue, emptyQueue());
  assert.deepEqual(applied.statements, []);
});

test('a failed apply keeps the queue and shows the error', () => {
  const pending = run([
    { type: 'editBegin', key: 'u0', column: 'name', value: 'a' },
    { type: 'editCommitFetched', index: 0, column: 'name', value: 'b', original: 'a' },
    { type: 'reviewOpen' },
    { type: 'applyStart' },
  ]);
  const failed = editReducer(pending, { type: 'applyResult', ok: false, error: 'deadlock' });
  assert.equal(failed.applying, false);
  assert.equal(failed.applyError, 'deadlock');
  assert.deepEqual(failed.queue.updates[0], { name: 'b' }, 'the edits survive the failure');
  assert.equal(failed.review, true, 'the dialog stays open so the error is visible');
});

// The preview is the payload of the apply, so a new preview and a plan error
// land together rather than one behind the other.
test('a preview and its error are one transition', () => {
  const planned = editReducer(emptyEditState(), {
    type: 'preview',
    statements: ['DELETE FROM t'],
    error: '',
  });
  assert.deepEqual(planned.statements, ['DELETE FROM t']);
  assert.equal(planned.planError, '');

  const failed = editReducer(planned, { type: 'preview', statements: [], error: 'no such column' });
  assert.deepEqual(failed.statements, []);
  assert.equal(failed.planError, 'no such column');
});

test('opening the review clears a stale apply error', () => {
  const stale = editReducer(emptyEditState(), { type: 'applyResult', ok: false, error: 'old' });
  assert.equal(stale.applyError, 'old');
  assert.equal(editReducer(stale, { type: 'reviewOpen' }).applyError, '');
});

// A reloaded card shares nothing with the rows that were queued, so the whole
// pipeline goes. A pending edit left over would be written against a row the
// user no longer sees.
test('a new result resets the whole pipeline', () => {
  const dirty = run([
    { type: 'editSelect', index: 2 },
    { type: 'editBegin', key: 'u2', column: 'name', value: 'a' },
    { type: 'editCommitFetched', index: 2, column: 'name', value: 'b', original: 'a' },
    { type: 'reviewOpen' },
    { type: 'preview', statements: ['UPDATE t SET name = 1'], error: '' },
    { type: 'applyResult', ok: false, error: 'boom' },
  ]);
  assert.notDeepEqual(dirty, emptyEditState());

  const reset = editReducer(dirty, { type: 'editReset' });
  assert.deepEqual(reset, emptyEditState());
});

test('cancelling an edit closes the editor but keeps the queue', () => {
  const dirty = run([
    { type: 'editBegin', key: 'u1', column: 'name', value: 'a' },
    { type: 'editCommitFetched', index: 1, column: 'name', value: 'b', original: 'a' },
    { type: 'editBegin', key: 'u3', column: 'note', value: '' },
  ]);
  const cancelled = editReducer(dirty, { type: 'editClose' });

  // A pending value already queued stays queued: closing the editor is a cancel
  // of the open cell, not a rollback of the row.
  assert.equal(cancelled.editing, null);
  assert.deepEqual(cancelled.queue.updates[1], { name: 'b' });
});

test('cancelling with no editor open is a no-op', () => {
  const state = run([{ type: 'editSelect', index: 0 }]);
  assert.equal(editReducer(state, { type: 'editClose' }), state);
});

// Cancelling an added row drops the row and, if its cell was the one open, the
// editor with it - a closed cell must not keep a stale draft behind it.
test('dropping an added row clears the editor if that row was open', () => {
  const added = run([{ type: 'rowAppend', id: 4, column: 'name' }]);
  assert.notEqual(added.editing, null);

  const dropped = editReducer(added, { type: 'rowDrop', id: 4 });
  assert.deepEqual(dropped.queue.inserts, []);
  assert.equal(dropped.editing, null);
});

test('dropping one added row leaves the others and their edit alone', () => {
  const state = run([
    { type: 'rowAppend', id: 1, column: 'name' },
    { type: 'editCommitFresh', id: 1, column: 'name', value: 'first' },
    { type: 'rowAppend', id: 2, column: 'name' },
  ]);
  assert.equal(state.editing?.key, 'n2');

  const dropped = editReducer(state, { type: 'rowDrop', id: 2 });
  assert.deepEqual(dropped.queue.inserts, [{ id: 1, values: { name: 'first' } }]);
  assert.equal(dropped.editing, null);

  // An editor on another row survives.
  const other = editReducer(
    run([
      { type: 'rowAppend', id: 1, column: 'name' },
      { type: 'rowAppend', id: 2, column: 'name' },
    ]),
    { type: 'rowDrop', id: 1 },
  );
  assert.equal(other.editing?.key, 'n2');
});

test('unknown actions leave the state alone', () => {
  const state = emptyEditState();
  assert.equal(editReducer(state, { type: 'nope' } as any), state);
});

// Undo pops one gesture at a time: a commit, a delete toggle and a clear are
// each reversible, and an empty stack is a no-op.
test('undo reverses queue gestures one at a time', () => {
  const committed = run([
    { type: 'editCommitFresh', id: 1, column: 'name', value: 'first' },
  ]);
  assert.equal(committed.undoStack.length, 1);

  const toggled = editReducer(committed, { type: 'deleteToggle', index: 0 });
  assert.equal(toggled.undoStack.length, 2);

  const undone = editReducer(toggled, { type: 'undo' });
  assert.equal(undone.undoStack.length, 1);
  assert.deepEqual(undone.queue, committed.queue);

  const undoneTwice = editReducer(undone, { type: 'undo' });
  assert.deepEqual(undoneTwice.queue, emptyQueue());
  assert.equal(undoneTwice.undoStack.length, 0);

  // Undo on an empty stack is a no-op, not a crash.
  const exhausted = editReducer(undoneTwice, { type: 'undo' });
  assert.deepEqual(exhausted.queue, emptyQueue());
});

// Clearing the whole queue is a gesture like any other, so it undoes too.
test('undo restores a cleared queue', () => {
  const cleared = run([
    { type: 'rowAppend', id: 1, column: 'name' },
    { type: 'queueClear' },
  ]);
  assert.equal(cleared.queue.inserts.length, 0);
  const restored = editReducer(cleared, { type: 'undo' });
  assert.equal(restored.queue.inserts.length, 1);
});

// A non-mutating gesture (selection, draft typing) must not grow the stack.
test('selection and drafts do not enter the undo stack', () => {
  const state = run([
    { type: 'editSelect', index: 2 },
    { type: 'editBegin', key: 'u2', column: 'name', value: 'v' },
    { type: 'editDraft', value: 'vv' },
    { type: 'editClose' },
  ]);
  assert.equal(state.undoStack.length, 0);
});
