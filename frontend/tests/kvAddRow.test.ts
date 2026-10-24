import assert from 'node:assert/strict';
import { test } from 'node:test';

import { kvAddRow } from '../src/kvAddRow.ts';

const draft = (member: string, id: string, fields: [string, string][] = []) => ({
  member,
  id,
  fields: fields.map(([name, value]) => ({ name, value })),
});

test('a set row is its member, trimmed', () => {
  assert.deepEqual(kvAddRow('set', draft('  go  ', '')), { row: { member: 'go' } });
  // A set has no id or fields; whatever the dialog kept there is not sent.
  assert.deepEqual(kvAddRow('set', draft('go', '1-0', [['a', 'b']])), { row: { member: 'go' } });
  assert.deepEqual(kvAddRow('set', draft('   ', '')), { error: '成员不能为空' });
});

test('a stream row keeps every filled field and lets Redis assign the id', () => {
  assert.deepEqual(kvAddRow('stream', draft('', '', [['kind', 'new'], ['n', '1']])), {
    row: { id: '', fields: [{ name: 'kind', value: 'new' }, { name: 'n', value: '1' }] },
  });
  assert.deepEqual(kvAddRow('stream', draft('', ' 9999999999999-0 ', [['kind', 'new']])), {
    row: { id: '9999999999999-0', fields: [{ name: 'kind', value: 'new' }] },
  });
});

test('empty field rows are dropped, an empty value is kept', () => {
  assert.deepEqual(kvAddRow('stream', draft('', '', [['kind', 'new'], ['', '']])), {
    row: { id: '', fields: [{ name: 'kind', value: 'new' }] },
  });
  assert.deepEqual(kvAddRow('stream', draft('', '', [['note', '']])), {
    row: { id: '', fields: [{ name: 'note', value: '' }] },
  });
});

test('a stream field name is required and unique', () => {
  assert.deepEqual(kvAddRow('stream', draft('', '', [['', 'orphan']])), {
    error: '字段名不能为空',
  });
  assert.deepEqual(kvAddRow('stream', draft('', '', [['kind', 'a'], [' kind ', 'b']])), {
    error: '字段 kind 重复',
  });
  assert.deepEqual(kvAddRow('stream', draft('', '', [['', '']])), {
    error: '至少需要一个字段',
  });
});

test('only sets and streams are appendable', () => {
  assert.deepEqual(kvAddRow('hash', draft('x', '', [['a', 'b']])), {
    error: '该类型不支持追加行',
  });
  assert.deepEqual(kvAddRow('list', draft('x', '', [])), { error: '该类型不支持追加行' });
});
