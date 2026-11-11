import assert from 'node:assert/strict';
import { test } from 'node:test';

import { findNamedParams, substituteParams } from '../src/lib/namedParams.ts';

test('finds placeholders in order, without duplicates', () => {
  assert.deepEqual(findNamedParams('SELECT * FROM t WHERE a = :from AND b = :to AND a > :from'), [
    'from',
    'to',
  ]);
});

test('skips strings, comments, casts and positional markers', () => {
  const sql = [
    "SELECT 'a :notparam' ::text, -- :also :not",
    '/* :nope */ `:tick`',
    'WHERE x = ::int AND y = :real1 AND z = :1',
  ].join('\n');
  assert.deepEqual(findNamedParams(sql), ['real1']);
});

test('substitution quotes text, leaves numbers bare, keeps unknown markers', () => {
  assert.equal(
    substituteParams('WHERE a = :name AND b = :n AND c = :missing', { name: "O'Neil", n: '42' }),
    "WHERE a = 'O''Neil' AND b = 42 AND c = :missing",
  );
});

test('substitution never touches casts or string contents', () => {
  assert.equal(
    substituteParams("SELECT 'a::int' ::text, x = :v", { v: '1' }),
    "SELECT 'a::int' ::text, x = 1",
  );
});

test('an empty value leaves the marker in place', () => {
  assert.equal(substituteParams('a = :x', { x: '' }), 'a = :x');
});

// MySQL's default sql_mode treats '\' as an escape: a value ending in one
// would swallow the closing quote and rewrite the statement. The MySQL family
// doubles the backslash (matching the server's own QuoteLiteral); ANSI engines
// keep it literal.
test('backslash doubling follows the engine family', () => {
  assert.equal(
    substituteParams("p = :v", { v: 'C:\\logs\\' }, 'mysql'),
    "p = 'C:\\\\logs\\\\'",
  );
  assert.equal(
    substituteParams("p = :v", { v: 'C:\\logs\\' }, 'mariadb'),
    "p = 'C:\\\\logs\\\\'",
  );
  assert.equal(
    substituteParams("p = :v", { v: 'C:\\logs\\' }, 'tidb'),
    "p = 'C:\\\\logs\\\\'",
  );
});

test('ANSI engines keep the backslash literal', () => {
  assert.equal(substituteParams("p = :v", { v: 'C:\\logs\\' }, 'postgresql'), "p = 'C:\\logs\\'");
  assert.equal(substituteParams("p = :v", { v: 'C:\\logs\\' }, 'sqlite'), "p = 'C:\\logs\\'");
  assert.equal(substituteParams("p = :v", { v: 'C:\\logs\\' }, 'sqlserver'), "p = 'C:\\logs\\'");
  // No kind yet (dialog answered before a connection was picked): ANSI.
  assert.equal(substituteParams("p = :v", { v: 'C:\\logs\\' }), "p = 'C:\\logs\\'");
});
