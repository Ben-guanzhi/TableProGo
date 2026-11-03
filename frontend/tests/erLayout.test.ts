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

// serialize flattens the whole layout (nodes, edges, canvas) to plain JSON so
// two layouts can be compared byte for byte — a Map key would otherwise hide
// order differences inside the object graph.
function serialize(layout: ReturnType<typeof computeLayout>): string {
  return JSON.stringify({
    nodes: layout.nodes.map(n => [n.table.name, n.x, n.y, n.width, n.height]),
    edges: layout.edges.map(e => [e.from, e.to, e.name]),
    width: layout.width,
    height: layout.height,
  });
}

// shuffle reorders a copy with a fixed Fisher-Yates seed: determinism must not
// depend on the order tables arrive in the model. The barycenter sweep sums
// neighbour positions in the neighbour-list order, which is built from the
// tables array — a float accumulation order that could in principle change
// the layout. This is the K-F3 contract: byte-identical output for any input
// order, so map/array iteration order can never leak back into the drawing.
function shuffled(tables: ReturnType<typeof table>[], seed: number): ReturnType<typeof table>[] {
  const arr = [...tables];
  let s = seed;
  const next = () => {
    s = (s * 1103515245 + 12345) & 0x7fffffff;
    return s;
  };
  for (let i = arr.length - 1; i > 0; i--) {
    const j = next() % (i + 1);
    [arr[i], arr[j]] = [arr[j], arr[i]];
  }
  return arr;
}

test('K-F3: the layout is byte-identical across input order (map order cannot leak)', () => {
  const base = [
    table('orders', ['customers']),
    table('order_items', ['orders', 'products']),
    table('customers'),
    table('products'),
    table('audit_log'),
    table('sessions', ['customers']),
    table('payments', ['orders', 'customers']),
    table('shipments', ['orders']),
  ];
  const reference = serialize(computeLayout(schema(base)));
  for (const seed of [1, 7, 42, 1234, 99999]) {
    const layout = computeLayout(schema(shuffled(base, seed)));
    assert.equal(
      serialize(layout),
      reference,
      `layout drifted for seed ${seed}: the drawing must not depend on input order`,
    );
  }
});

test('K-F3: the full layout (edges included) is byte-identical on repeat', () => {
  const model = schema([
    table('orders', ['customers']),
    table('order_items', ['orders', 'products']),
    table('customers'),
    table('products'),
  ]);
  assert.equal(serialize(computeLayout(model)), serialize(computeLayout(model)));
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
