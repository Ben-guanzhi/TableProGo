import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  cellProblem,
  depsOf,
  firstProblem,
  insertReference,
  moveCell,
  newCellId,
  problemText,
  referencesOf,
} from '../src/lib/notebook.ts';

const cell = (sql: string, id = 'c' + sql.length) => ({ id, sql });

test('referencesOf reads cell tokens once, in order', () => {
  assert.deepEqual(referencesOf('SELECT * FROM {{cell_2}} a JOIN {{cell_1}} b ON 1=1'), [2, 1]);
  // Duplicates collapse: the kernel would inline one CTE, so the UI must agree.
  assert.deepEqual(referencesOf('{{cell_1}} {{cell_1}}'), [1]);
  // Whitespace inside the braces is allowed, exactly like the kernel.
  assert.deepEqual(referencesOf('{{ cell_3 }}'), [3]);
  // Anything else is not a reference.
  assert.deepEqual(referencesOf('{{ name }} and {"a":1}'), []);
});

test('cellProblem accepts a first cell with no references', () => {
  assert.equal(cellProblem([cell('SELECT 1')], 0), null);
});

test('cellProblem accepts a backwards reference', () => {
  assert.equal(cellProblem([cell('SELECT 1 AS a'), cell('SELECT a FROM {{cell_1}}')], 1), null);
});

test('cellProblem flags self, forward, missing and empty references', () => {
  // Self.
  assert.deepEqual(cellProblem([cell('SELECT * FROM {{cell_1}}')], 0), { kind: 'self', cell: 1 });
  // Forward: the message must say the cell comes later, because that is the fix.
  const fwd = cellProblem([cell('SELECT * FROM {{cell_2}}'), cell('SELECT 1')], 0);
  assert.deepEqual(fwd, { kind: 'forward', cell: 1, ref: 2 });
  assert.match(problemText(fwd!), /在后面/);
  // A cell that is empty cannot be built on.
  assert.deepEqual(cellProblem([cell('  '), cell('SELECT * FROM {{cell_1}}')], 1), {
    kind: 'emptyRef',
    cell: 2,
    ref: 1,
  });
  // The running cell itself being empty.
  assert.deepEqual(cellProblem([cell('')], 0), { kind: 'empty', cell: 1 });
});

test('depsOf is transitive and deepest-first', () => {
  const cells = [cell('SELECT 1 AS a'), cell('SELECT a FROM {{cell_1}}'), cell('SELECT a FROM {{cell_2}}')];
  // Cell 3 depends on 2, which depends on 1: CTE order must be 1 then 2.
  assert.deepEqual(depsOf(cells, 2), [1, 2]);
  // Only the referenced branch is included.
  const branching = [cell('SELECT 1'), cell('SELECT 2'), cell('SELECT * FROM {{cell_2}}')];
  assert.deepEqual(depsOf(branching, 2), [2]);
});

test('insertReference appends a token with a separating space', () => {
  assert.equal(insertReference('SELECT * FROM t', 1), 'SELECT * FROM t {{cell_1}}');
  // No double space when the text already ends with one.
  assert.equal(insertReference('SELECT * FROM t ', 2), 'SELECT * FROM t {{cell_2}}');
  assert.equal(insertReference('', 3), '{{cell_3}}');
});

test('newCellId avoids collisions', () => {
  const id = newCellId([{ id: 'c1', sql: '' }, { id: 'c2', sql: '' }]);
  assert.ok(id !== 'c1' && id !== 'c2');
  assert.equal(newCellId([{ id: 'x', sql: '' }]), 'c1');
});

test('moveCell reorders without mutating and ignores bad indexes', () => {
  const cells = ['a', 'b', 'c'];
  assert.deepEqual(moveCell(cells, 0, 2), ['b', 'c', 'a']);
  assert.deepEqual(cells, ['a', 'b', 'c'], 'the input array must not be mutated');
  // Out-of-range moves are a no-op rather than a crash.
  assert.deepEqual(moveCell(cells, 0, 9), cells);
  assert.deepEqual(moveCell(cells, -1, 0), cells);
});

test('firstProblem finds the earliest broken cell, so run-all can refuse up front', () => {
  // A notebook is a pipeline: discovering the breakage on cell 3 after running 1–2
  // leaves a half-finished state. The pre-check reports cell 2 and nothing runs.
  const cells = [cell('SELECT 1 AS a'), cell('SELECT * FROM {{cell_3}}'), cell('SELECT 1')];
  const found = firstProblem(cells);
  assert.ok(found);
  assert.equal(found!.index, 1);
  assert.equal(found!.problem.kind, 'forward');

  // A runnable notebook reports nothing.
  assert.equal(firstProblem([cell('SELECT 1'), cell('SELECT * FROM {{cell_1}}')]), null);
  assert.equal(firstProblem([]), null);
});