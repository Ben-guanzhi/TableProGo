import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  FK_ACTIONS,
  foreignKeyEditRows,
  formatColumnList,
  indexEditRows,
  normalizeFKAction,
  parseColumnList,
} from '../src/lib/structureEdit.ts';
import type { ForeignKeyInfo, IndexInfo } from '../src/types/index.ts';

const index = (over: Partial<IndexInfo>): IndexInfo => ({
  name: 'idx_orders_customer',
  kind: 'index',
  unique: false,
  primary: false,
  columns: ['customer_id'],
  ...over,
});

const fk = (over: Partial<ForeignKeyInfo>): ForeignKeyInfo => ({
  name: 'fk_orders_customer',
  columns: ['customer_id'],
  refSchema: 'public',
  refTable: 'customers',
  refColumns: ['id'],
  onDelete: 'NO ACTION',
  onUpdate: 'NO ACTION',
  ...over,
});

test('parseColumnList trims fragments and drops empties', () => {
  assert.deepEqual(parseColumnList(' a , ,b ,'), ['a', 'b']);
  assert.deepEqual(parseColumnList(''), []);
  assert.deepEqual(parseColumnList(' , '), []);
});

test('formatColumnList is the exact inverse shape the rows show', () => {
  assert.equal(formatColumnList(['a', 'b']), 'a, b');
  assert.deepEqual(parseColumnList(formatColumnList(['a', 'b'])), ['a', 'b']);
});

test('the index tab hides the primary key and constraint autoindexes', () => {
  const rows = indexEditRows([
    index({ name: 'PRIMARY', primary: true, kind: 'primary' }),
    index({ name: 'sqlite_autoindex_orders_1' }),
    index({ name: 'idx_a' }),
  ]);
  assert.deepEqual(
    rows.map(r => r.name),
    ['idx_a'],
  );
});

test('expression and predicate indexes are locked, not editable', () => {
  const rows = indexEditRows([
    index({ name: 'idx_expr', columns: [], parts: ['lower(email)'] }),
    index({ name: 'idx_partial', columns: ['a'], predicate: 'status = 1' }),
    index({ name: 'idx_plain' }),
  ]);
  assert.equal(rows[0].locked, true);
  assert.match(rows[0].reason!, /表达式/);
  assert.equal(rows[1].locked, true);
  assert.match(rows[1].reason!, /部分索引/);
  assert.equal(rows[2].locked, false);
  assert.equal(rows[2].reason, undefined);
});

test('a plain index round-trips: same columns text parses back to the catalog list', () => {
  const info = index({ columns: ['customer_id', 'created_at'] });
  const [row] = indexEditRows([info]);
  assert.equal(row.columnsText, 'customer_id, created_at');
  assert.deepEqual(parseColumnList(row.columnsText), info.columns);
});

test('catalog FK actions normalize onto the select options', () => {
  assert.equal(normalizeFKAction('NO_ACTION'), 'NO ACTION');
  assert.equal(normalizeFKAction('cascade'), 'CASCADE');
  assert.equal(normalizeFKAction('set null'), 'SET NULL');
  assert.equal(normalizeFKAction(''), '');
  for (const a of FK_ACTIONS) assert.equal(normalizeFKAction(a), a);
});

test('an untouched foreign key keeps its action values inside FK_ACTIONS', () => {
  const [row] = foreignKeyEditRows([fk({ onDelete: 'NO_ACTION', onUpdate: 'cascade' })]);
  assert.ok((FK_ACTIONS as readonly string[]).includes(row.onDelete));
  assert.ok((FK_ACTIONS as readonly string[]).includes(row.onUpdate));
  assert.deepEqual(parseColumnList(row.columnsText), ['customer_id']);
  assert.equal(row.refTable, 'customers');
  assert.deepEqual(parseColumnList(row.refColumnsText), ['id']);
});
