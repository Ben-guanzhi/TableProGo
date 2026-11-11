import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  buildFKAddPayload,
  buildFKDropPayload,
  buildFKRollbackPayload,
  fkDefinitionFromER,
  fkLabel,
  normalizeRefColumns,
  parseFKEditAction,
  trimList,
  type FKEditTarget,
} from '../src/lib/erFk.ts';
import type { ERFK } from '../src/types/index.ts';

const target: FKEditTarget = { connId: 'c1', database: 'db', schema: 'public', table: 'book' };

const fk = (over: Partial<ERFK>): ERFK => ({
  name: 'fk_book_author',
  columns: ['author_id'],
  refSchema: 'public',
  refTable: 'author',
  refColumns: ['id'],
  onDelete: 'CASCADE',
  onUpdate: 'NO ACTION',
  ...over,
});

test('parseFKEditAction accepts the three operations case-insensitively', () => {
  assert.equal(parseFKEditAction('add'), 'add');
  assert.equal(parseFKEditAction(' DROP '), 'drop');
  assert.equal(parseFKEditAction('Rollback'), 'rollback');
  assert.throws(() => parseFKEditAction('rename'), /add \/ drop \/ rollback/);
  assert.throws(() => parseFKEditAction(''), /add \/ drop \/ rollback/);
});

test('trimList drops empty fragments and surrounding whitespace', () => {
  assert.deepEqual(trimList([' a ', '', 'b']), ['a', 'b']);
  assert.deepEqual(trimList(undefined), []);
  assert.deepEqual(trimList(['', '  ']), []);
});

test('normalizeRefColumns pads a short reference from the source columns', () => {
  assert.deepEqual(normalizeRefColumns(['id'], ['author_id', 'tenant_id']), ['id', 'tenant_id']);
  assert.deepEqual(normalizeRefColumns(['a', 'b', 'c'], ['a', 'b']), ['a', 'b']);
  assert.deepEqual(normalizeRefColumns([], ['a']), ['a']);
  assert.deepEqual(normalizeRefColumns(undefined, ['a', 'b']), ['a', 'b']);
  assert.deepEqual(normalizeRefColumns(['x'], []), []);
});

test('fkDefinitionFromER normalizes the actions and trims the fields', () => {
  const def = fkDefinitionFromER(fk({
    refSchema: ' ',
    refColumns: [],
    onDelete: 'cascade',
    onUpdate: 'NO_ACTION',
  }));
  assert.equal(def.name, 'fk_book_author');
  assert.deepEqual(def.columns, ['author_id']);
  assert.equal(def.refSchema, '');
  assert.equal(def.refTable, 'author');
  assert.deepEqual(def.refColumns, ['author_id']);
  assert.equal(def.onDelete, 'CASCADE');
  assert.equal(def.onUpdate, 'NO ACTION');
});

test('buildFKAddPayload sends the standardized key and honors preview', () => {
  const payload = buildFKAddPayload(target, fk({ onDelete: 'cascade' }), true);
  assert.equal(payload.action, 'add');
  assert.equal(payload.preview, true);
  assert.equal(payload.connId, 'c1');
  assert.equal(payload.table, 'book');
  assert.equal(payload.name, 'fk_book_author');
  assert.deepEqual(payload.columns, ['author_id']);
  assert.deepEqual(payload.refColumns, ['id']);
  assert.equal(payload.onDelete, 'CASCADE');
  assert.equal(payload.onUpdate, 'NO ACTION');
  assert.equal(buildFKAddPayload(target, fk({})).preview, false);
});

test('buildFKAddPayload refuses an unusable key', () => {
  assert.throws(() => buildFKAddPayload(target, fk({ name: '' })), /外键名为空/);
  assert.throws(() => buildFKAddPayload(target, fk({ columns: [] })), /没有本表列/);
  assert.throws(() => buildFKAddPayload(target, fk({ refTable: '' })), /没有引用表/);
});

test('buildFKDropPayload trims the name and refuses an empty one', () => {
  assert.deepEqual(buildFKDropPayload(target, '  fk_book_author  '), {
    connId: 'c1',
    database: 'db',
    schema: 'public',
    table: 'book',
    action: 'drop',
    preview: false,
    fkName: 'fk_book_author',
  });
  assert.throws(() => buildFKDropPayload(target, '   '), /外键名/);
});

test('buildFKRollbackPayload carries the definition so the undo can be undone', () => {
  const payload = buildFKRollbackPayload(target, { fkName: 'fk_book_author', rollbackOf: 'drop', fk: fk({}) }, true);
  assert.equal(payload.action, 'rollback');
  assert.equal(payload.rollbackOf, 'drop');
  assert.equal(payload.name, 'fk_book_author');
  assert.deepEqual(payload.columns, ['author_id']);
  assert.equal(payload.refTable, 'author');
  assert.equal(payload.onDelete, 'CASCADE');

  const withoutDef = buildFKRollbackPayload(target, { fkName: 'fk_book_author', rollbackOf: 'add' });
  assert.equal(withoutDef.name, undefined);
  assert.equal(withoutDef.rollbackOf, 'add');

  assert.throws(
    () => buildFKRollbackPayload(target, { fkName: 'x', rollbackOf: 'rename' }),
    /应为 add \/ drop/,
  );
  assert.throws(() => buildFKRollbackPayload(target, { fkName: '  ', rollbackOf: 'add' }), /外键名/);
});

test('fkLabel names the edge for confirmations', () => {
  assert.equal(fkLabel(fk({})), 'fk_book_author（author_id → author）');
  assert.equal(fkLabel(fk({ name: '', columns: [], refTable: '' })), '(未命名)（ → ?）');
});
