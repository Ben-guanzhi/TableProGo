import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  applyMapping,
  entriesToText,
  mappingTextToEntries,
  parseMappingText,
  parseSkipList,
  validateMapping,
} from '../src/lib/importMapping.ts';

test('a mapping line may use =, => or : and a bare source skips', () => {
  assert.deepEqual(parseMappingText('id = person_id\nname => name\nextra:\n# comment\n'), [
    { source: 'id', target: 'person_id' },
    { source: 'name', target: 'name' },
    { source: 'extra', target: '' },
  ]);
});

test('blank lines are ignored and the later line wins for a repeated source', () => {
  const entries = parseMappingText('id => a\n\nid => b\n');
  assert.deepEqual(entries, [{ source: 'id', target: 'b' }]);
});

test('an unknown source and a duplicate target are both reported', () => {
  const cols = ['id', 'name'];
  assert.deepEqual(validateMapping([{ source: 'nope', target: 'id' }], cols), [
    '映射的源列 "nope" 不在文件列中（第 1 条映射）',
  ]);
  assert.deepEqual(validateMapping([{ source: 'id', target: 'x' }, { source: 'name', target: 'x' }], cols), [
    '映射目标列 "x" 被使用了多次（第 2 条映射）',
  ]);
  assert.deepEqual(validateMapping([{ source: 'id', target: '' }, { source: 'name', target: '' }], cols), [
    '所有源列都被跳过，没有可导入的列',
  ]);
});

test('an empty mapping is not authoritative and validates cleanly', () => {
  assert.deepEqual(validateMapping([], ['id']), []);
});

test('applying a mapping keeps file order and reports the skipped sources', () => {
  const cols = ['id', 'name', 'score', 'extra'];
  const entries = mappingTextToEntries('score => score\nid => person_id', cols);
  const applied = applyMapping(entries, cols);
  assert.deepEqual(applied.cols, ['person_id', 'score']);
  assert.deepEqual(applied.keep, [0, 2]);
  assert.deepEqual(applied.skipColumns, ['name', 'extra']);
  assert.deepEqual(applied.columnMap, { id: 'person_id', score: 'score' });
});

test('a source that is not in the file is not applied, even when named', () => {
  const cols = ['id'];
  const applied = applyMapping(parseMappingText('id => id\nmissing => x'), cols);
  assert.deepEqual(applied.cols, ['id']);
  assert.deepEqual(applied.skipColumns, []);
});

test('entries round-trip through the editor text form', () => {
  const cols = ['id', 'name'];
  const entries = mappingTextToEntries('id => person_id\nname', cols);
  assert.equal(entriesToText(entries), 'id => person_id\nname');
});

test('a skip list accepts both separators and trims', () => {
  assert.deepEqual(parseSkipList(' a , b ，c '), ['a', 'b', 'c']);
  assert.deepEqual(parseSkipList(''), []);
});
