import assert from 'node:assert/strict';
import { test } from 'node:test';

import { cellMagic } from '../src/cellMagic.ts';

const magic = (column: string, text: string, raw: unknown = text) => cellMagic(column, text, raw);

test('a whole-cell URL becomes a link; prose containing one does not', () => {
  assert.deepEqual(magic('homepage', 'https://example.com/a?b=1'), {
    kind: 'link',
    url: 'https://example.com/a?b=1',
    display: 'https://example.com/a?b=1',
  });
  assert.equal(magic('bio', 'visit https://example.com today'), null);
  assert.equal(magic('site', 'example.com'), null, 'a bare domain is not a link');
});

test('money columns format numbers with separators and fixed cents', () => {
  assert.deepEqual(magic('unit_price', '1234.5', 1234.5), { kind: 'number', display: '1,234.50' });
  assert.deepEqual(magic('total', '1234', 1234), { kind: 'number', display: '1,234.00' });
  assert.deepEqual(magic('amount', '-9876543.21', -9876543.21), {
    kind: 'number',
    display: '-9,876,543.21',
  });
  // The Chinese column words count too.
  assert.equal(magic('账户余额', '50', 50)?.kind, 'number');
});

test('an explicit decimal fraction keeps its digits through the grouping', () => {
  // DECIMAL(x,4) text must not round to two decimals — the fraction is kept
  // verbatim and only the integer part gets separators (pure string work, so
  // a 22-digit value survives intact — P3-2's reason decimals travel as text).
  assert.equal(cellMagic('fee', '0.1234', '0.1234')?.display, '0.1234');
  assert.equal(
    cellMagic('amount', '9999999999999999999999.5', '9999999999999999999999.5')?.display,
    '9,999,999,999,999,999,999,999.5',
  );
});

test('a non-money column never reformats, and money text stays text', () => {
  assert.equal(magic('id', '1234', 1234), null);
  // Text in a money column is not a number: leave it exactly as stored.
  assert.equal(magic('total', 'twelve', 'twelve'), null);
});

test('timestamps simplify to "date time", with the zone kept visible', () => {
  assert.deepEqual(magic('created_at', '2026-10-24T12:00:00Z', '2026-10-24T12:00:00Z'), {
    kind: 'date',
    display: '2026-10-24 12:00:00 UTC',
  });
  assert.deepEqual(magic('ts', '2026-10-24 12:00:00.123456', '2026-10-24 12:00:00.123456'), {
    kind: 'date',
    display: '2026-10-24 12:00:00',
  });
  assert.equal(
    cellMagic('offset', '2026-10-24T12:00:00+08:00', 'x')?.display,
    '2026-10-24 12:00:00 +08:00',
    'a non-UTC offset must not be silently dropped',
  );
  // A date without a time is not a timestamp, and text-shaped numbers neither.
  assert.equal(magic('birthday', '2026-10-24', '2026-10-24'), null);
});

test('JSON cells carry a folded form for the tooltip, compact for the cell', () => {
  const m = cellMagic('payload', '{"a":1,"b":[1,2]}', '{"a":1,"b":[1,2]}');
  assert.ok(m && m.kind === 'json');
  if (m.kind !== 'json') return;
  assert.equal(m.display, '{"a":1,"b":[1,2]}', 'the cell must not change bytes');
  assert.equal(m.pretty, '{\n  "a": 1,\n  "b": [\n    1,\n    2\n  ]\n}');
  assert.equal(magic('blob', '{not json', 'x'), null);
  assert.equal(magic('blob', '{oops}', 'x'), null);
});

test('empty cells earn no magic', () => {
  assert.equal(magic('total', '', ''), null);
  assert.equal(magic('url', '', ''), null);
});
