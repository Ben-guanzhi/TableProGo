import assert from 'node:assert/strict';
import { test } from 'node:test';

import { computeLayout, NODE_W, PAD, type ERSchema } from '../src/datagrid/erLayout.ts';

const table = (name: string, refs: string[] = []) => ({
  name,
  schema: 'public',
  columns: [{ name: 'id', type: 'int', nullable: false, isPrimaryKey: true }],
  foreignKeys: refs.map(r => ({
    name: `fk_${name}_${r}`,
    columns: [`${r}_id`],
    refSchema: 'public',
    refTable: r,
    refColumns: ['id'],
    onDelete: 'NO ACTION',
    onUpdate: 'NO ACTION',
  })),
});

const schema = (tables: ReturnType<typeof table>[]): ERSchema => ({ tables });

// levels(node) reads the x a table got: column index * (NODE_W + GAP) + PAD.
const columnOf = (layout: ReturnType<typeof computeLayout>, name: string) =>
  (layout.byName.get(name)!.x - PAD) / (NODE_W + 80);

// crossings counts edge pairs whose endpoint rows disagree in order, per
// column pair — the number a barycenter sweep is trying to push down.
function crossings(layout: ReturnType<typeof computeLayout>): number {
  const byCol = new Map<number, { name: string; y: number }[]>();
  for (const n of layout.nodes) {
    const col = (n.x - PAD) / (NODE_W + 80);
    if (!byCol.has(col)) byCol.set(col, []);
    byCol.get(col)!.push({ name: n.table.name, y: n.y });
  }
  const rowOf = new Map<string, number>();
  for (const col of byCol.values()) {
    col.sort((a, b) => a.y - b.y);
    col.forEach((n, i) => rowOf.set(n.name, i));
  }
  let count = 0;
  for (let i = 0; i < layout.edges.length; i++) {
    for (let j = i + 1; j < layout.edges.length; j++) {
      const e1 = layout.edges[i];
      const e2 = layout.edges[j];
      const c1 = columnOf(layout, e1.from);
      if (c1 !== columnOf(layout, e2.from) || c1 - 1 !== columnOf(layout, e1.to) || c1 - 1 !== columnOf(layout, e2.to)) {
        continue; // only comparable edges span the same column pair
      }
      const a1 = rowOf.get(e1.from)! - rowOf.get(e1.to)!;
      const a2 = rowOf.get(e2.from)! - rowOf.get(e2.to)!;
      if (a1 * a2 < 0) count++;
    }
  }
  return count;
}

test('barycenter removes the crossings alphabetical order makes', () => {
  // x1,x2→beta; x3→alpha; x4→gamma. Alphabetical parents (alpha, beta, gamma)
  // cross the x3→alpha edge twice; the sweeps must land on a drawing where
  // the referenced side follows its referrers — whichever side moves.
  const layout = computeLayout(
    schema([
      table('x1', ['beta']),
      table('x2', ['beta']),
      table('x3', ['alpha']),
      table('x4', ['gamma']),
      table('alpha'),
      table('beta'),
      table('gamma'),
    ]),
  );

  assert.equal(columnOf(layout, 'x1'), 1);
  assert.equal(columnOf(layout, 'alpha'), 0);
  assert.equal(crossings(layout), 0);
});

test('the layout is deterministic: same schema, same drawing', () => {
  const model = schema([
    table('orders', ['customers']),
    table('order_items', ['orders', 'products']),
    table('customers'),
    table('products'),
    table('audit_log'),
  ]);
  const a = computeLayout(model);
  const b = computeLayout(model);
  assert.deepEqual(
    a.nodes.map(n => [n.table.name, n.x, n.y]),
    b.nodes.map(n => [n.table.name, n.x, n.y]),
  );
});

test('circular references still lay out (no overflow) and every table is placed', () => {
  const layout = computeLayout(
    schema([table('a', ['b']), table('b', ['c']), table('c', ['a'])]),
  );
  assert.equal(layout.nodes.length, 3);
  for (const n of layout.nodes) {
    assert.ok(Number.isFinite(n.x) && Number.isFinite(n.y));
  }
});

test('isolated tables keep a slot without disturbing connected ones', () => {
  const layout = computeLayout(
    schema([table('solo'), table('child', ['parent']), table('parent')]),
  );
  assert.equal(layout.nodes.length, 3);
  // child and parent are adjacent columns (child one RIGHT of parent).
  assert.equal(columnOf(layout, 'child'), columnOf(layout, 'parent') + 1);
});
