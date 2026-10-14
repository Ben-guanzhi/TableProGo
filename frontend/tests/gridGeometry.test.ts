import assert from 'node:assert/strict';
import { test } from 'node:test';

import { computeColumnWidths, MAX_WIDTH, MIN_WIDTH } from '../src/datagrid/geometry.ts';
import { optionId } from '../src/datagrid/picker.ts';
import { opNeedsValue } from '../src/gridView.ts';
import type { Column, QueryResult } from '../src/types/index.ts';

const cols = (...names: string[]): Column[] => names.map((name) => ({ name, type: 'text', nullable: true }));

const result = (columns: Column[], rows: any[][] = []): QueryResult => ({
  columns,
  rows,
  rowCount: rows.length,
  affectedRows: 0,
  lastInsertId: 0,
  executionTime: 0,
});

// Narrow headers are floored, otherwise a column called "id" would be too thin
// to show both the name and its type label.
test('a narrow header is widened to the floor', () => {
  assert.deepEqual(computeColumnWidths(result(cols('id'))), [MIN_WIDTH]);
});

// A medium header is measured from its own length: 20 chars is 168px.
test('a medium header is measured from its own length', () => {
  assert.deepEqual(computeColumnWidths(result(cols('a'.repeat(20)))), [168]);
});

// A very long header would otherwise claim the viewport on its own.
test('a header cannot exceed the ceiling', () => {
  assert.deepEqual(computeColumnWidths(result(cols('a'.repeat(80)))), [MAX_WIDTH]);
});

// The widest cell wins over the header: 20 chars of text is 164px.
test('a wide cell wins over a narrow header', () => {
  assert.deepEqual(computeColumnWidths(result(cols('id'), [['a'.repeat(20)]])), [164]);
});

// A single enormous value must not claim the viewport, so the sample is capped
// before measuring: 46 measured chars is 346px, whatever the real length.
test('a cell is measured with a cap', () => {
  const widths = computeColumnWidths(result(cols('id'), [['a'.repeat(500)]]));
  assert.deepEqual(widths, [346]);
  assert.ok(widths[0] < MAX_WIDTH);
});

// Sampling stops after 50 rows, so a late row cannot affect the layout.
test('rows past the sample are not measured', () => {
  const rows = [['x'], ...Array.from({ length: 49 }, () => ['x']), ['a'.repeat(500)]];
  assert.equal(rows.length, 51);
  assert.deepEqual(computeColumnWidths(result(cols('id'), rows)), [MIN_WIDTH]);
});

// Nulls and empty strings measure nothing, and the floor still holds.
test('blank cells do not shrink a column below the floor', () => {
  assert.deepEqual(computeColumnWidths(result(cols('id'), [[null], ['', undefined], ['']])), [MIN_WIDTH]);
});

// One list is open at a time, so its rows can be addressed by position alone.
test('the picker addresses its options by position', () => {
  assert.equal(optionId(0), 'fk-option-0');
  assert.equal(optionId(42), 'fk-option-42');
});

// eq/ne/contains are meaningless without a value, so the menu keeps a draft box
// for them only.
test('only value-taking operators ask for a value', () => {
  assert.equal(opNeedsValue('eq'), true);
  assert.equal(opNeedsValue('ne'), true);
  assert.equal(opNeedsValue('contains'), true);
  assert.equal(opNeedsValue('null'), false);
  assert.equal(opNeedsValue('notNull'), false);
});
