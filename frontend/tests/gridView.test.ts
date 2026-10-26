import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  addRow,
  buildView,
  cellText,
  compareCells,
  copyValues,
  countEdits,
  displayCell,
  dropRow,
  emptyQueue,
  matchesFilter,
  queueCounts,
  setRowValue,
  toRowChanges,
  toggleDelete,
  withEdit,
} from '../src/gridView.ts';
import type { CellEdits } from '../src/gridView.ts';

const columns = [{ name: 'id' }, { name: 'qty' }];
// Three rows, the middle one with a NULL qty: enough to pin the NULL rule.
const rows: any[][] = [
  [1, 30],
  [2, null],
  [3, 10],
];

const columnOf = (rows: any[][], view: number[], column: string) =>
  view.map(i => rows[i][columns.findIndex(c => c.name === column)]);

test('NULLs sort last in both directions', () => {
  assert.deepEqual(columnOf(rows, buildView(rows, columns, {}, { column: 'qty', dir: 'asc' }), 'qty'), [10, 30, null]);
  assert.deepEqual(columnOf(rows, buildView(rows, columns, {}, { column: 'qty', dir: 'desc' }), 'qty'), [30, 10, null]);
});

test('buildView keeps original row indices so an edit cannot change row', () => {
  const view = buildView(rows, columns, {}, { column: 'qty', dir: 'desc' });
  // The top visible row is the original row 0 (id 1, qty 30), not index 0 of the view.
  assert.equal(view[0], 0);
  const edits = withEdit({}, view[0], 'id', '1', '99');
  assert.deepEqual(edits[0], { id: '99' });
  assert.equal(rows[0][0], 1, 'the fetched row itself is untouched until apply');
});

test('filters and sorting combine, and a filtered row keeps its own index', () => {
  const view = buildView(rows, columns, { qty: { op: 'notNull', value: '' } }, { column: 'qty', dir: 'desc' });
  assert.deepEqual(view, [0, 2]);
  assert.deepEqual(columnOf(rows, view, 'qty'), [30, 10]);
});

test('an unapplied edit is what the cell shows', () => {
  const pending = displayCell(30, '31');
  assert.deepEqual(pending, { text: '31', isNull: false, edited: true });
  assert.deepEqual(displayCell(null, undefined), { text: '', isNull: true, edited: false });
  // The original stays reachable, which is what the tooltip shows.
  assert.equal(displayCell(null, '7').text, '7');
});

test('typing the original value back drops the edit', () => {
  // The grid compares against cellText of the fetched value, so a NULL cell
  // starts from an empty string rather than the word NULL.
  const edits: CellEdits = withEdit(withEdit({}, 1, 'qty', '', '5'), 1, 'qty', '', '');
  assert.equal(countEdits(edits), 0);
  assert.deepEqual(edits, {});
});

test('cancelling the editor leaves the earlier edit standing', () => {
  // Escape closes the editor without committing, so the pending value - not the
  // fetched one - is what remains on screen.
  const edits = withEdit({}, 2, 'qty', '10', '11');
  assert.equal(displayCell(rows[2][1], edits[2]?.qty).text, '11');
});

test('filter operators', () => {
  assert.equal(matchesFilter(null, { op: 'null', value: '' }), true);
  assert.equal(matchesFilter(1, { op: 'null', value: '' }), false);
  assert.equal(matchesFilter('Alpha', { op: 'eq', value: ' alpha ' }), true);
  assert.equal(matchesFilter('Alpha', { op: 'ne', value: 'alpha' }), false);
  assert.equal(matchesFilter('Alpha', { op: 'contains', value: 'PH' }), true);
  // An empty filter value narrows nothing, so the grid does not blank out.
  assert.equal(matchesFilter('anything', { op: 'eq', value: '   ' }), true);
});

test('compareCells orders numbers as numbers', () => {
  assert.ok(compareCells(2, 10, 1) < 0);
  assert.ok(compareCells('b', 'a', 1) > 0);
  assert.equal(compareCells(null, null, -1), 0);
});

// P3-2: an exact-decimal column crosses the wire as digit TEXT, and the grid
// must still ORDER it numerically (9 < 10, not "10" < "9") while displaying
// the text verbatim. The column's isDecimal flag is what makes both possible.
test('a decimal column sorts by value while the cells stay text', () => {
  const decCols = [{ name: 'price', isDecimal: true }];
  const decRows: any[][] = [['9'], ['10'], ['2.5'], [null], ['9999999999999999999999.5']];
  const asc = buildView(decRows, decCols, {}, { column: 'price', dir: 'asc' });
  // 2.5 < 9 < 10 < the 22-digit value; NULL last whatever the direction.
  assert.deepEqual(asc, [2, 0, 1, 4, 3]);
  const desc = buildView(decRows, decCols, {}, { column: 'price', dir: 'desc' });
  assert.deepEqual(desc, [4, 1, 0, 2, 3]);
  // Displayed text is untouched by the sort: no float ever touched the digits.
  assert.equal(cellText(decRows[asc[3]][0]), '9999999999999999999999.5');
  // Without the flag the same values stay lexical — proving the flag is what
  // changed the answer.
  const plain = buildView(decRows, [{ name: 'price' }], {}, { column: 'price', dir: 'asc' });
  assert.deepEqual(plain, [1, 2, 0, 4, 3]);
});

// Two numbers compare as numbers even in a column that also holds text, while a
// number against text is read as its digits. That is the rule the grid has always
// used; the cached key a sort builds must not quietly change it.
test('a column mixing numbers and text compares each pair by its own kinds', () => {
  assert.ok(compareCells(9, 'b', 1) < 0);
  assert.ok(compareCells('b', 9, 1) > 0);
  assert.ok(compareCells(10, 9, 1) > 0);
  // What the mixed column looks like once every cell has gone through the key:
  // the same three answers, because the number keeps both forms.
  const mixed: any[][] = [[1, 'b'], [2, 10], [3, 9]];
  const asc = buildView(mixed, columns, {}, { column: 'qty', dir: 'asc' });
  const desc = buildView(mixed, columns, {}, { column: 'qty', dir: 'desc' });
  assert.deepEqual(asc, [...desc].reverse(), 'the directions stay mirrors of each other');
});

// The rows a sort leaves tied come out in the order they were fetched, which is
// what makes a second click on another column predictable.
test('a sort does not reorder rows that tie', () => {
  const tied: any[][] = [
    [1, 'x'],
    [2, 'x'],
    [3, 'x'],
  ];
  assert.deepEqual(buildView(tied, columns, {}, { column: 'qty', dir: 'desc' }), [0, 1, 2]);
});

// A filter is answered by the column it names; if the result has no such column
// there is nothing to match, so no row survives.
test('a filter naming a column the result lacks keeps no rows', () => {
  assert.deepEqual(buildView(rows, columns, { missing: { op: 'eq', value: '10' } }, null), []);
  // Combined with a filter that does resolve, it still decides the whole view.
  assert.deepEqual(
    buildView(rows, columns, { qty: { op: 'notNull', value: '' }, missing: { op: 'eq', value: '10' } }, null),
    [],
  );
});

// The queue is what travels to the builder: deletes first so a freed unique
// value is available, the edited cells next, and the new rows last.
const editQueueCell = (queue: ReturnType<typeof emptyQueue>, row: number, column: string, original: string, value: string) => ({
  ...queue,
  updates: withEdit(queue.updates, row, column, original, value),
});

test('the queue sends deletes first, then edited cells, then new rows', () => {
  let queue = editQueueCell(emptyQueue(), 1, 'qty', '', '9');
  queue = toggleDelete(queue, 2);
  queue = setRowValue(addRow(queue, 7), 7, 'qty', '3');
  assert.deepEqual(toRowChanges(queue, rows), [
    { kind: 'delete', original: [3, 10], edits: [] },
    { original: [2, null], edits: [{ column: 'qty', value: '9' }] },
    { kind: 'insert', original: [], edits: [{ column: 'qty', value: '3' }] },
  ]);
});

test('marking a row deleted drops its pending edits, and marking it again undoes it', () => {
  let queue = editQueueCell(emptyQueue(), 0, 'qty', '30', '31');
  queue = toggleDelete(queue, 0);
  assert.deepEqual(queue.updates, {}, 'a row that is going away has nothing to update');
  assert.deepEqual(queue.deletes, [0]);
  queue = toggleDelete(queue, 0);
  assert.deepEqual(queue, emptyQueue(), 'the mark is the undo');
});

test('leaving a new row cell empty leaves that column out of the insert', () => {
  let queue = addRow(emptyQueue(), 1);
  queue = setRowValue(queue, 1, 'qty', '5');
  queue = setRowValue(queue, 1, 'id', '');
  assert.deepEqual(toRowChanges(queue, rows), [
    { kind: 'insert', original: [], edits: [{ column: 'qty', value: '5' }] },
  ]);
  queue = setRowValue(queue, 1, 'qty', '');
  assert.deepEqual(queue.inserts[0].values, {});
  // A row with nothing typed is still a queued change, so the builder is the
  // one that says why it cannot be applied - the grid never drops it quietly.
  assert.equal(queueCounts(addRow(emptyQueue(), 9)).inserts, 1);
  assert.deepEqual(dropRow(queue, 1), emptyQueue());
});

test('a second new row cannot shift the values of the first', () => {
  let queue = setRowValue(addRow(emptyQueue(), 1), 1, 'qty', 'a');
  queue = setRowValue(addRow(queue, 2), 2, 'qty', 'b');
  assert.deepEqual(queue.inserts.map(row => row.values), [{ qty: 'a' }, { qty: 'b' }]);
  queue = dropRow(queue, 2);
  assert.deepEqual(toRowChanges(queue, rows), [
    { kind: 'insert', original: [], edits: [{ column: 'qty', value: 'a' }] },
  ]);
});

// A duplicate is an insert seeded from the source row: the key goes to the
// database, and a NULL is left out rather than typed as the word NULL.
test('copyValues carries the values over but not the key', () => {
  assert.deepEqual(copyValues([2, 'b', null], [{ name: 'id' }, { name: 'name' }, { name: 'qty' }], ['id']), {
    name: 'b',
  });
  // The result may name a column in another case than the catalog does.
  assert.deepEqual(copyValues([2, 'b'], [{ name: 'ID' }, { name: 'name' }], ['id']), { name: 'b' });
  // A keyless table has nothing to drop, so the copy is a plain duplicate.
  assert.deepEqual(copyValues([2, 'b'], [{ name: 'id' }, { name: 'name' }], []), { id: '2', name: 'b' });
});

test('a seeded row joins the queue as an insert of those columns', () => {
  const wide = [{ name: 'id' }, { name: 'name' }, { name: 'qty' }];
  const queue = addRow(emptyQueue(), 4, copyValues([2, 'b', 20], wide, ['id']));
  assert.deepEqual(toRowChanges(queue, []), [
    {
      kind: 'insert',
      original: [],
      edits: [
        { column: 'name', value: 'b' },
        { column: 'qty', value: '20' },
      ],
    },
  ]);
  assert.equal(queueCounts(queue).inserts, 1);
});

test('the queue counts each kind of change', () => {
  let queue = editQueueCell(emptyQueue(), 0, 'qty', '30', '31');
  queue = editQueueCell(queue, 0, 'id', '1', '2');
  queue = toggleDelete(queue, 2);
  queue = addRow(queue, 5);
  assert.deepEqual(queueCounts(queue), { updates: 2, inserts: 1, deletes: 1, total: 4 });
  assert.deepEqual(queueCounts(emptyQueue()), { updates: 0, inserts: 0, deletes: 0, total: 0 });
});
