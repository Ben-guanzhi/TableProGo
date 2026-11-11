import assert from 'node:assert/strict';
import { test } from 'node:test';

import { toTSV } from '../src/gridView.ts';

test('tsv joins with tabs and quotes fields that hold separators', () => {
  const cols = [{ name: 'a' }, { name: 'b' }] as any;
  const rows = [['1', 'plain'], ['2', 'has\ttab'], ['3', 'say "hi"']];
  const out = toTSV(cols, rows);
  const lines = out.split('\r\n');
  assert.deepEqual(lines[0].split('\t'), ['a', 'b']);
  assert.equal(lines[1], '1\tplain');
  assert.equal(lines[2], '2\t"has\ttab"');
  assert.equal(lines[3], '3\t"say ""hi"""');
});
