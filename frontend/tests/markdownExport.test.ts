import assert from 'node:assert/strict';
import { test } from 'node:test';
import { toMarkdown } from '../src/gridView.ts';

const columns = [{ name: 'id' }, { name: 'note' }];

test('a markdown table is a header, a separator, then one line per row', () => {
  assert.equal(
    toMarkdown(columns, [
      [1, 'a'],
      [2, null],
    ]),
    ['| id | note |', '| --- | --- |', '| 1 | a |', '| 2 |  |'].join('\n'),
  );
});

test('an export with no rows still leaves a table to paste into', () => {
  assert.equal(toMarkdown(columns, []), '| id | note |\n| --- | --- |');
});

// A pipe would end the column and a newline would end the row, so a value that
// looks like the syntax has to be escaped rather than written out.
test('a value that looks like table syntax cannot break the table', () => {
  assert.equal(toMarkdown([{ name: 'a | b' }], [['x | y']]), '| a \\| b |\n| --- |\n| x \\| y |');
  assert.equal(toMarkdown([{ name: 'n' }], [['第一行\n第二行']]), '| n |\n| --- |\n| 第一行<br>第二行 |');
  assert.equal(toMarkdown([{ name: 'n' }], [['第一行\r\n第二行']]), '| n |\n| --- |\n| 第一行<br>第二行 |');
});

test('a structured value is exported as the text the grid shows', () => {
  assert.equal(toMarkdown([{ name: 'j' }], [[{ a: 1 }]]), '| j |\n| --- |\n| {"a":1} |');
});
