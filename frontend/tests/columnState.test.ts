import assert from 'node:assert/strict';
import { test } from 'node:test';

import { columnReducer, emptyColumnState } from '../src/datagrid/columnState.ts';
import type { ColumnAction, ColumnState } from '../src/datagrid/columnState.ts';

const run = (actions: ColumnAction[]): ColumnState =>
  actions.reduce(columnReducer, emptyColumnState());

test('a fresh grid has no view applied', () => {
  const state = emptyColumnState();
  assert.deepEqual(state.overrides, {});
  assert.equal(state.locate, '');
  assert.equal(state.activeMatch, 0);
  assert.equal(state.sort, null);
  assert.deepEqual(state.filters, {});
  assert.equal(state.menu, null);
  assert.equal(state.filterDraft, '');
  assert.equal(state.exportMenu, null);
});

// Typing restarts the walk, or a stale match index would highlight the wrong
// column as soon as the result set changes under the box.
test('typing in the locate box restarts the walk', () => {
  const state = run([
    { type: 'locateType', value: 'na' },
    { type: 'locateNext', total: 4 },
    { type: 'locateNext', total: 4 },
    { type: 'locateType', value: 'nam' },
  ]);
  assert.equal(state.locate, 'nam');
  assert.equal(state.activeMatch, 0);
});

test('Enter walks the matches and wraps', () => {
  let state = run([{ type: 'locateType', value: 'a' }]);
  for (const expected of [1, 2, 0]) {
    state = columnReducer(state, { type: 'locateNext', total: 3 });
    assert.equal(state.activeMatch, expected);
  }
});

test('Enter with no matches does nothing', () => {
  const before = run([{ type: 'locateType', value: 'zzz' }]);
  assert.equal(columnReducer(before, { type: 'locateNext', total: 0 }), before);
});

test('the locate box clears its walk with it', () => {
  const state = run([
    { type: 'locateType', value: 'a' },
    { type: 'locateNext', total: 3 },
    { type: 'locateClear' },
  ]);
  assert.equal(state.locate, '');
  assert.equal(state.activeMatch, 0);
});

// A second click on the active direction lifts the sort, so the header reads as
// a toggle instead of requiring a separate clear button.
test('sorting toggles: set, re-sorted, lifted', () => {
  let state = run([{ type: 'sortToggle', column: 'name', dir: 'asc' }]);
  assert.deepEqual(state.sort, { column: 'name', dir: 'asc' });

  state = columnReducer(state, { type: 'sortToggle', column: 'name', dir: 'desc' });
  assert.deepEqual(state.sort, { column: 'name', dir: 'desc' });

  state = columnReducer(state, { type: 'sortToggle', column: 'name', dir: 'desc' });
  assert.equal(state.sort, null);
});

test('sorting one column does not disturb another filter', () => {
  const state = run([
    { type: 'filterSet', column: 'name', filter: { op: 'eq', value: 'x' } },
    { type: 'sortToggle', column: 'id', dir: 'asc' },
  ]);
  assert.deepEqual(state.sort, { column: 'id', dir: 'asc' });
  assert.deepEqual(state.filters, { name: { op: 'eq', value: 'x' } });
});

test('a filter can be set and lifted', () => {
  let state = run([{ type: 'filterSet', column: 'name', filter: { op: 'contains', value: 'bo' } }]);
  assert.deepEqual(state.filters, { name: { op: 'contains', value: 'bo' } });

  state = columnReducer(state, { type: 'filterSet', column: 'name', filter: null });
  assert.deepEqual(state.filters, {});
});

// Reopening the menu must not start from an empty box: whatever was applied
// before is what the user edits next.
test('opening the menu seeds the draft from the applied filter', () => {
  const withFilter = run([
    { type: 'filterSet', column: 'name', filter: { op: 'contains', value: 'bo' } },
  ]);
  const opened = columnReducer(withFilter, { type: 'menuOpen', x: 10, y: 20, column: 'name' });
  assert.equal(opened.filterDraft, 'bo');
  assert.equal(opened.menu?.column, 'name');
  assert.equal(opened.menu?.op, null);
});

test('opening the menu with no filter starts empty', () => {
  const opened = columnReducer(emptyColumnState(), { type: 'menuOpen', x: 1, y: 2, column: 'note' });
  assert.equal(opened.filterDraft, '');
});

// The split that matters: operators with a value keep the menu open, the rest
// apply and close. Getting this wrong either loses the typed value or leaves a
// menu open that nothing dismisses.
test('a value-taking operator keeps the menu open', () => {
  const opened = run([{ type: 'menuOpen', x: 1, y: 2, column: 'name' }]);
  for (const op of ['eq', 'ne', 'contains'] as const) {
    const state = columnReducer(opened, { type: 'menuPickOp', op });
    assert.equal(state.menu?.column, 'name', `${op} keeps the menu open`);
    assert.equal(state.menu?.op, op, `${op} is the pending operator`);
    assert.deepEqual(state.filters, {}, `${op} applies nothing until a value is typed`);
  }
});

test('an operator without a value applies and closes', () => {
  for (const op of ['null', 'notNull'] as const) {
    const state = run([{ type: 'menuOpen', x: 1, y: 2, column: 'name' }]);
    const picked = columnReducer(state, { type: 'menuPickOp', op });
    assert.equal(picked.menu, null, `${op} closes the menu`);
    assert.deepEqual(picked.filters, { name: { op, value: '' } });
  }
});

test('picking an operator with no menu open is a no-op', () => {
  const state = emptyColumnState();
  assert.equal(columnReducer(state, { type: 'menuPickOp', op: 'eq' }), state);
});

test('clearing the view clears the sort and the filters but not the widths', () => {
  const state = run([
    { type: 'widthSet', name: 'name', width: 300 },
    { type: 'locateType', value: 'x' },
    { type: 'sortToggle', column: 'name', dir: 'asc' },
    { type: 'filterSet', column: 'note', filter: { op: 'null', value: '' } },
    { type: 'viewsClear' },
  ]);
  assert.equal(state.sort, null);
  assert.deepEqual(state.filters, {});
  assert.deepEqual(state.overrides, { name: 300 });
  assert.equal(state.locate, 'x');
});

test('a manual width is stored and can be lifted back to the sampled width', () => {
  let state = run([{ type: 'widthSet', name: 'name', width: 300 }]);
  assert.deepEqual(state.overrides, { name: 300 });

  state = columnReducer(state, { type: 'widthSet', name: 'name', width: 120 });
  assert.deepEqual(state.overrides, { name: 120 });

  state = columnReducer(state, { type: 'widthReset', name: 'name' });
  assert.deepEqual(state.overrides, {});
});

// Widths survive a new result: the user sized a column for their own screen, and
// a reload has no business resetting it.
test('a new result keeps manual widths', () => {
  const state = run([
    { type: 'widthSet', name: 'name', width: 300 },
    { type: 'columnReset' },
  ]);
  assert.deepEqual(state.overrides, { name: 300 });
  assert.equal(state.menu, null);
  assert.equal(state.exportMenu, null);
});

// Sort and filter are views over the columns, not the rows, so they survive a
// reload: a name that no longer exists simply matches nothing.
test('a new result keeps the sort and the filters', () => {
  const state = run([
    { type: 'sortToggle', column: 'name', dir: 'asc' },
    { type: 'filterSet', column: 'note', filter: { op: 'null', value: '' } },
    { type: 'columnReset' },
  ]);
  assert.deepEqual(state.sort, { column: 'name', dir: 'asc' });
  assert.deepEqual(state.filters, { note: { op: 'null', value: '' } });
});

test('the export menu opens and closes', () => {
  let state = columnReducer(emptyColumnState(), { type: 'exportMenuOpen', left: 5, top: 6 });
  assert.deepEqual(state.exportMenu, { left: 5, top: 6 });
  state = columnReducer(state, { type: 'exportMenuClose' });
  assert.equal(state.exportMenu, null);
  assert.equal(columnReducer(state, { type: 'exportMenuClose' }), state);
});

test('the sort can be lifted on its own', () => {
  const sorted = run([
    { type: 'filterSet', column: 'note', filter: { op: 'null', value: '' } },
    { type: 'sortToggle', column: 'name', dir: 'asc' },
  ]);

  const lifted = columnReducer(sorted, { type: 'sortClear' });
  assert.equal(lifted.sort, null);
  assert.deepEqual(lifted.filters, { note: { op: 'null', value: '' } });
  assert.deepEqual(lifted.overrides, {});
});

test('lifting with nothing sorted is a no-op', () => {
  const state = emptyColumnState();
  assert.equal(columnReducer(state, { type: 'sortClear' }), state);
});

test('unknown actions leave the state alone', () => {
  const state = emptyColumnState();
  assert.equal(columnReducer(state, { type: 'nope' } as any), state);
});
