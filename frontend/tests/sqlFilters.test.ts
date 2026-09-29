import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  addFilter,
  describeFilter,
  dropFilter,
  filterIsComplete,
  filterNeedsValue,
  isNumericType,
  opsForType,
} from '../src/sqlFilters.ts';
import type { SqlFilter } from '../src/types/index.ts';

test('a numeric column is not offered a text comparison', () => {
  assert.ok(!opsForType('integer').includes('contains'));
  assert.ok(!opsForType('DECIMAL(10,2)').includes('contains'));
  assert.ok(!opsForType('bigint unsigned').includes('contains'));
  assert.ok(opsForType('text').includes('contains'));
  assert.ok(opsForType('varchar(20)').includes('contains'));
  // The empty type of a column the driver did not describe keeps everything.
  assert.ok(opsForType('').includes('contains'));
  assert.equal(isNumericType('double precision'), true);
});

test('a condition is complete once its comparison has something to compare', () => {
  assert.equal(filterIsComplete({ column: '', op: 'eq', value: '1' }), false);
  // "等于" with nothing in it is a real search: the empty string, or NULL in a
  // numeric column.
  assert.equal(filterIsComplete({ column: 'name', op: 'eq', value: '' }), true);
  assert.equal(filterIsComplete({ column: 'name', op: 'ne', value: 'x' }), true);
  // The one-sided comparisons would match no row at all against an empty value.
  assert.equal(filterIsComplete({ column: 'qty', op: 'gt', value: '  ' }), false);
  assert.equal(filterIsComplete({ column: 'qty', op: 'contains', value: 'a' }), true);
  assert.equal(filterIsComplete({ column: 'qty', op: 'null', value: '' }), true);
  assert.equal(filterNeedsValue('notNull'), false);
});

test('conditions keep the order they were added in', () => {
  const first: SqlFilter = { column: 'name', op: 'contains', value: 'gear' };
  const second: SqlFilter = { column: 'id', op: 'lt', value: '5' };
  const filters = dropFilter(addFilter(addFilter([], first), second), 0);
  assert.deepEqual(filters, [second]);
  // Dropping by position cannot take the wrong chip when two read the same.
  const twins = addFilter(addFilter([], first), { ...first });
  assert.deepEqual(dropFilter(twins, 1), [first]);
});

test('a chip says what it filters in the words the bar offered', () => {
  assert.equal(describeFilter({ column: 'name', op: 'contains', value: 'gear' }), 'name 包含 gear');
  assert.equal(describeFilter({ column: 'qty', op: 'notNull', value: '' }), 'qty 不为空');
});
