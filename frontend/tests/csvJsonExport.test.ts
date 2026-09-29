import assert from 'node:assert/strict';
import { test } from 'node:test';
import { toCSV, toJSON } from '../src/gridView.ts';

const columns = [{ name: 'id' }, { name: 'note' }];
const BOM = '\ufeff'; // what makes Excel read UTF-8 headers

test('a csv file is a header line then one line per row, ending with CRLF between', () => {
  assert.equal(toCSV(columns, [[1, 'a'], [2, null]]), BOM + 'id,note\r\n1,a\r\n2,');
});

test('a header-only export still says what the columns were', () => {
  assert.equal(toCSV(columns, []), BOM + 'id,note');
});

// A comma ends a field, a newline ends a row and a quote ends the quoting, so a
// value holding any of them has to arrive wrapped with its quotes doubled.
test('a value that looks like csv syntax cannot add a column or a row', () => {
  assert.equal(toCSV([{ name: 'a,b' }], [['say "hi"'], ['两\r\n行']]), BOM + '"a,b"\r\n"say ""hi"""\r\n"两\r\n行"');
});

// The grid can hand over a row that is shorter than the header (a result read
// before the last column arrived). Writing what the row holds would shift the
// later values into the wrong columns, so the missing ones stay empty.
test('a short row leaves the trailing fields empty rather than shifting left', () => {
  assert.equal(toCSV(columns, [[1]]), BOM + 'id,note\r\n1,');
});

test('a structured value is written as the text the grid shows', () => {
  assert.equal(toCSV([{ name: 'j' }], [[{ a: 1 }]]), BOM + 'j\r\n"{""a"":1}"');
});

// The JSON export is the one a program reads back, so the types the driver sent
// survive: numbers stay numbers and NULL stays null.
test('json export keeps the value types and writes null for NULL', () => {
  const rows = [[1, 'a'], [2, null]];
  assert.deepEqual(JSON.parse(toJSON(columns, rows)), [
    { id: 1, note: 'a' },
    { id: 2, note: null },
  ]);
});

test('json export is indented for reading in an editor', () => {
  assert.equal(toJSON([{ name: 'id' }], [[7]]), '[\n  {\n    "id": 7\n  }\n]');
});

test('an empty result is an empty json array', () => {
  assert.equal(toJSON(columns, []), '[]');
});
