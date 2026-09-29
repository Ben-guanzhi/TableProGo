import assert from 'node:assert/strict';
import { test } from 'node:test';
import { cycleActive, describeCandidate, filterCandidates, foreignKeyFor } from '../src/gridView.ts';

const fk = (columns: string[], refColumns: string[]) => ({ columns, refColumns });

test('a reference to one column of one table is what a cell can offer', () => {
  const fks = [fk(['customer_id'], ['id']), fk(['a', 'b'], ['x', 'y'])];
  assert.deepEqual(foreignKeyFor(fks, 'customer_id'), { columns: ['customer_id'], refColumns: ['id'] });
});

// Picking half a composite key would store a value that cannot match any row, so
// the grid keeps plain text entry for those columns.
test('a column inside a composite key gets no candidate list', () => {
  const fks = [fk(['order_id', 'product_id'], ['id', 'id'])];
  assert.equal(foreignKeyFor(fks, 'product_id'), null);
});

test('an unrelated column gets no candidate list', () => {
  assert.equal(foreignKeyFor([fk(['warehouse_id'], ['id'])], 'note'), null);
  assert.equal(foreignKeyFor([], 'note'), null);
});

// MySQL and SQL Server hand back the name with their own case, while the result
// header carries what the query selected.
test('the lookup ignores which side capitalised the name', () => {
  assert.ok(foreignKeyFor([fk(['Customer_ID'], ['ID'])], 'customer_id'));
  assert.ok(foreignKeyFor([fk(['customer_id'], ['id'])], 'CUSTOMER_ID'));
});

test('the arrow keys walk the list and wrap at both ends', () => {
  assert.equal(cycleActive(0, 1, 5), 1);
  assert.equal(cycleActive(4, 1, 5), 0);
  assert.equal(cycleActive(0, -1, 5), 4);
  assert.equal(cycleActive(-1, 1, 5), 0);
  assert.equal(cycleActive(2, 7, 5), 4);
});

test('an empty list has no row to highlight', () => {
  assert.equal(cycleActive(0, 1, 0), -1);
});

test('a candidate is described by its value, and by its row when there is one', () => {
  assert.equal(describeCandidate({ value: '7', label: 'Alice' }), '7 · Alice');
  assert.equal(describeCandidate({ value: '7' }), '7');
  assert.equal(describeCandidate({ value: '7', label: '' }), '7');
});

// Typing in the cell searches the list it opened, and a name counts as much as
// the key does - the person is looking for a row, not for a digit.
test('typing narrows the list by value or by description', () => {
  const list = [
    { value: '1', label: 'Alice' },
    { value: '2', label: 'Bob' },
    { value: '11', label: 'Alicia' },
  ];
  assert.deepEqual(filterCandidates(list, ''), list);
  assert.deepEqual(filterCandidates(list, 'ali').map(c => c.value), ['1', '11']);
  assert.deepEqual(filterCandidates(list, ' 2 ').map(c => c.value), ['2']);
  assert.deepEqual(filterCandidates(list, 'BOB').map(c => c.value), ['2']);
  assert.deepEqual(filterCandidates(list, 'zzz'), []);
});
