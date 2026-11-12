import assert from 'node:assert/strict';
import { test } from 'node:test';

import { argsOf, cellText, initialValues, missingRequired } from '../src/lib/pluginConsole.ts';

// W17: the console's pure logic. The interesting cases are the ones a form gets wrong:
// a number that silently becomes a string, an omitted optional field that becomes "",
// and a NULL cell that renders as empty.

test('initialValues pre-fills declared defaults and blanks the rest', () => {
  const values = initialValues([
    { name: 'topK', default: '10' },
    { name: 'collection', required: true },
  ]);
  assert.equal(values.topK, '10');
  assert.equal(values.collection, '');
});

test('argsOf sends a number field as a number', () => {
  const args = argsOf([{ name: 'topK', kind: 'number' }], { topK: ' 25 ' });
  assert.equal(args.topK, 25);
});

test('argsOf keeps a non-number field as a string', () => {
  // A vector is a comma-separated string; turning it into a number would corrupt it.
  const args = argsOf([{ name: 'vector' }], { vector: '0.1,0.2,0.3' });
  assert.equal(args.vector, '0.1,0.2,0.3');
});

test('argsOf omits empty optional fields', () => {
  // "not given" must stay distinguishable from "given as empty": a plugin uses its own
  // default for the former.
  const args = argsOf([{ name: 'filter' }, { name: 'topic', default: 'orders' }], {
    filter: '   ',
    topic: 'orders',
  });
  assert.deepEqual(args, { topic: 'orders' });
});

test('argsOf does not coerce a non-digit number field', () => {
  const args = argsOf([{ name: 'limit', kind: 'number' }], { limit: 'all' });
  assert.equal(args.limit, 'all');
});

test('missingRequired names the first blank required field', () => {
  const params = [
    { name: 'topic', required: true },
    { name: 'value', required: true, label: '消息内容' },
  ];
  assert.equal(missingRequired(params, { topic: '', value: 'x' }), 'topic');
  assert.equal(missingRequired(params, { topic: 't', value: '  ' }), '消息内容');
  assert.equal(missingRequired(params, { topic: 't', value: 'x' }), '');
});

test('cellText spells NULL instead of an empty cell', () => {
  assert.equal(cellText(null), 'NULL');
  assert.equal(cellText(undefined), 'NULL');
  assert.equal(cellText(''), '');
  assert.equal(cellText(0), '0');
  assert.equal(cellText(false), 'false');
});

test('cellText renders an object as JSON', () => {
  assert.equal(cellText({ a: 1 }), '{"a":1}');
});