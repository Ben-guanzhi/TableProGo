import assert from 'node:assert/strict';
import { test } from 'node:test';

import { kvEditTarget } from '../src/kvEdit.ts';

test('a string maps its value cell onto the key itself', () => {
  assert.deepEqual(kvEditTarget('string', ['hello'], 'value'), {
    identity: 'key',
    id: '',
    column: 'value',
    current: 'hello',
    label: '字符串值',
  });
  assert.equal(kvEditTarget('string', ['hello'], 'member'), null);
});

test('hash fields are addressed by field name', () => {
  assert.deepEqual(kvEditTarget('hash', ['name', 'Ada'], 'value'), {
    identity: 'field',
    id: 'name',
    column: 'value',
    current: 'Ada',
    label: '字段 name',
  });
  // The field column identifies the row; it is not a writable value.
  assert.equal(kvEditTarget('hash', ['name', 'Ada'], 'field'), null);
});

test('list items are addressed by their index', () => {
  assert.deepEqual(kvEditTarget('list', [1, 'second'], 'value'), {
    identity: 'index',
    id: '1',
    column: 'value',
    current: 'second',
    label: '第 2 项',
  });
});

test('sorted-set members are addressed by member, editing the score', () => {
  assert.deepEqual(kvEditTarget('zset', ['alice', 1.5], 'score'), {
    identity: 'member',
    id: 'alice',
    column: 'score',
    current: '1.5',
    label: '成员 alice 的分数',
  });
  assert.equal(kvEditTarget('zset', ['alice', 1.5], 'member'), null);
});

test('sets, streams and missing rows are not writable', () => {
  assert.equal(kvEditTarget('set', ['x'], 'member'), null);
  assert.equal(kvEditTarget('stream', ['1-1 f=v'], 'entry'), null);
  assert.equal(kvEditTarget('string', undefined, 'value'), null);
});

test('null values become an empty draft', () => {
  assert.equal(kvEditTarget('hash', ['field', null], 'value')?.current, '');
  assert.equal(kvEditTarget('list', [0, undefined], 'value')?.current, '');
});
