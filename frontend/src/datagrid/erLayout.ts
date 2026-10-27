import type { ERSchema, ERTable } from '../types';

// Geometry constants shared by the layout and the SVG renderer so the two never
// drift. Sizes are in SVG user units (unscaled); the renderer applies its own
// pan/zoom transform on top.
export const NODE_W = 220;
export const HEADER_H = 28;
export const ROW_H = 22;
const GAP_X = 80;
const GAP_Y = 36;
export const PAD = 28;

// One laid-out node: the table plus its top-left corner and box size.
export interface ERNode {
  table: ERTable;
  x: number;
  y: number;
  width: number;
  height: number;
}

// One foreign-key edge between two tables in the diagram.
export interface EREdge {
  from: string; // source table name
  to: string; // referenced table name
  name: string;
  columns: string[];
  refColumns: string[];
}

export interface ERLayout {
  nodes: ERNode[];
  edges: EREdge[];
  width: number;
  height: number;
  byName: Map<string, ERNode>;
}

// The pixel height of a table box: a header plus one row per column (at least
// one so an empty table still draws).
export function nodeHeight(t: ERTable): number {
  return HEADER_H + Math.max(t.columns.length, 1) * ROW_H + 10;
}

// The vertical centre of one column row inside its node, in the node's own
// coordinate space. Used to anchor an edge at the right column.
export function columnRowY(index: number): number {
  return HEADER_H + index * ROW_H + ROW_H / 2 + 4;
}

// computeLayout arranges the schema as a left-to-right graph: a table's column
// is one past the deepest table it references, so children sit left of their
// parents. Tables with no parents form the first column. A recursion-depth guard
// makes circular foreign keys resolve to level 0 instead of overflowing.
//
// Within a column the order starts alphabetical and then goes through a few
// barycenter sweeps: each column reorders by the average row of its neighbours
// in the adjacent column, which pulls connected tables level with each other.
// Minimizing crossings outright is NP-hard even to check, so the heuristic is
// the point — a fixed small number of sweeps keeps the layout deterministic
// and O(sweeps · edges).
export function computeLayout(model: ERSchema): ERLayout {
  const tables = model.tables;
  const tableByName = new Map(tables.map(t => [t.name, t]));

  const level = new Map<string, number>();
  const visiting = new Set<string>();
  const levelOf = (name: string, depth: number): number => {
    if (depth > 1000) return 0;
    const cached = level.get(name);
    if (cached !== undefined) return cached;
    if (visiting.has(name)) return 0; // cycle: break it
    visiting.add(name);
    const t = tableByName.get(name);
    let l = 0;
    if (t) {
      for (const fk of t.foreignKeys) {
        if (fk.refTable !== name && tableByName.has(fk.refTable)) {
          l = Math.max(l, levelOf(fk.refTable, depth + 1) + 1);
        }
      }
    }
    visiting.delete(name);
    level.set(name, l);
    return l;
  };
  for (const t of tables) levelOf(t.name, 0);

  const maxLevel = tables.reduce((m, t) => Math.max(m, level.get(t.name) ?? 0), 0);
  const columns: ERTable[][] = Array.from({ length: maxLevel + 1 }, () => []);
  for (const t of tables) columns[level.get(t.name) ?? 0].push(t);
  for (const col of columns) col.sort((a, b) => a.name.localeCompare(b.name));

  // Undirected adjacency over the drawn edges (self-references add nothing to
  // a layering and cross-schema refs are not in the diagram).
  const neighbors = new Map<string, string[]>();
  const link = (a: string, b: string) => {
    if (!neighbors.has(a)) neighbors.set(a, []);
    if (!neighbors.has(b)) neighbors.set(b, []);
    neighbors.get(a)!.push(b);
    neighbors.get(b)!.push(a);
  };
  for (const t of tables) {
    for (const fk of t.foreignKeys) {
      if (fk.refTable === t.name || !tableByName.has(fk.refTable)) continue;
      link(t.name, fk.refTable);
    }
  }

  // The barycenter sweeps. Each pass walks the columns in one direction and
  // reorders every column by its neighbours' average row in the adjacent one;
  // alternating direction lets pulls from both sides settle.
  const order: string[][] = columns.map(col => col.map(t => t.name));
  const levelOfName = (name: string) => level.get(name) ?? 0;
  for (let sweep = 0; sweep < 4; sweep++) {
    const forward = sweep % 2 === 0;
    const cols = [...order.keys()];
    if (!forward) cols.reverse();
    for (const c of cols) {
      const adj = forward ? c - 1 : c + 1;
      if (adj < 0 || adj >= order.length || order[c].length < 2) continue;
      const pos = new Map<string, number>();
      order.forEach((col, ci) => col.forEach((name, i) => pos.set(name, i)));
      order[c] = order[c]
        .map((name, i) => {
          let sum = 0;
          let count = 0;
          for (const nb of neighbors.get(name) ?? []) {
            if (levelOfName(nb) === adj) {
              sum += pos.get(nb) ?? 0;
              count++;
            }
          }
          // A table without neighbours in the adjacent column keeps its
          // current slot as the sort key, so isolated tables do not drift.
          return { name, key: count > 0 ? sum / count : i };
        })
        .sort((a, b) => a.key - b.key || a.name.localeCompare(b.name))
        .map(w => w.name);
    }
  }

  const nodeList: ERNode[] = [];
  const byName = new Map<string, ERNode>();
  let totalH = PAD;
  columns.forEach((_, ci) => {
    let y = PAD;
    for (const name of order[ci]) {
      const t = tableByName.get(name)!;
      const h = nodeHeight(t);
      const x = PAD + ci * (NODE_W + GAP_X);
      const node: ERNode = { table: t, x, y, width: NODE_W, height: h };
      nodeList.push(node);
      byName.set(t.name, node);
      y += h + GAP_Y;
    }
    totalH = Math.max(totalH, y);
  });
  const totalW = PAD * 2 + maxLevel * (NODE_W + GAP_X) + NODE_W;

  const edges: EREdge[] = [];
  for (const t of tables) {
    const n = byName.get(t.name);
    if (!n) continue;
    for (const fk of t.foreignKeys) {
      if (fk.refTable === t.name) continue; // self-reference: a loop adds no insight
      if (!byName.has(fk.refTable)) continue; // cross-schema ref not in this diagram
      edges.push({
        from: t.name,
        to: fk.refTable,
        name: fk.name,
        columns: fk.columns,
        refColumns: fk.refColumns,
      });
    }
  }

  return { nodes: nodeList, edges, width: totalW, height: totalH + PAD, byName };
}
