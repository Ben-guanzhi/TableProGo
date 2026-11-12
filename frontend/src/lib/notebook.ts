// Notebook cell helpers, kept pure so the ordering rules are testable without a UI.
//
// The same rules the kernel enforces (backwards-only references, reads only) are
// mirrored here for IMMEDIATE feedback: a cell that points at a later cell should
// show that while it is being typed, not after a run fails. The kernel stays the
// authority — this only predicts what it will say.

export interface CellLike {
  id: string;
  sql: string;
  title?: string;
}

/** The regex the kernel uses, kept identical on purpose. */
const REF = /\{\{\s*cell_(\d+)\s*\}\}/g;

/** referencesOf lists the 1-based cell numbers a cell's SQL mentions, deduplicated. */
export const referencesOf = (sql: string): number[] => {
  const seen = new Set<number>();
  const out: number[] = [];
  for (const m of sql.matchAll(REF)) {
    const n = Number(m[1]);
    if (!Number.isFinite(n) || n < 1 || seen.has(n)) continue;
    seen.add(n);
    out.push(n);
  }
  return out;
};

export type CellProblem =
  | { kind: 'self'; cell: number }
  | { kind: 'forward'; cell: number; ref: number }
  | { kind: 'missing'; cell: number; ref: number }
  | { kind: 'empty'; cell: number }
  | { kind: 'emptyRef'; cell: number; ref: number };

/**
 * cellProblem explains why one cell would not run, or null when it is fine.
 *
 * index is 0-based; messages use 1-based numbers because that is what the cell
 * headers show and what `{{cell_N}}` means.
 */
export const cellProblem = (cells: CellLike[], index: number): CellProblem | null => {
  const cell = cells[index];
  if (!cell) return { kind: 'missing', cell: index + 1, ref: index + 1 };
  if (cell.sql.trim() === '') return { kind: 'empty', cell: index + 1 };
  for (const ref of referencesOf(cell.sql)) {
    if (ref === index + 1) return { kind: 'self', cell: index + 1 };
    if (ref > index + 1) return { kind: 'forward', cell: index + 1, ref };
    const dep = cells[ref - 1];
    if (!dep) return { kind: 'missing', cell: index + 1, ref };
    if (dep.sql.trim() === '') return { kind: 'emptyRef', cell: index + 1, ref };
  }
  return null;
};

/** problemText renders a CellProblem for display. */
export const problemText = (p: CellProblem): string => {
  switch (p.kind) {
    case 'self':
      return `单元格 ${p.cell} 引用了自己`;
    case 'forward':
      return `单元格 ${p.cell} 引用了 {{cell_${p.ref}}}，它在后面；笔记本只能引用前面的单元格`;
    case 'missing':
      return `单元格 ${p.cell} 引用的 {{cell_${p.ref}}} 不存在`;
    case 'empty':
      return `单元格 ${p.cell} 是空的`;
    case 'emptyRef':
      return `单元格 ${p.cell} 引用的 {{cell_${p.ref}}} 是空的`;
  }
};

/** depsOf returns the transitive dependencies of a cell, deepest first. */
export const depsOf = (cells: CellLike[], index: number): number[] => {
  const order: number[] = [];
  const seen = new Set<number>();
  const walk = (i: number): void => {
    for (const ref of referencesOf(cells[i]?.sql ?? '')) {
      const dep = ref - 1;
      if (dep < 0 || dep >= i || seen.has(dep)) continue;
      seen.add(dep);
      walk(dep);
      order.push(ref);
    }
  };
  walk(index);
  return order;
};

/** insertReference appends a reference token at the caret. */
export const insertReference = (sql: string, ref: number): string =>
  `${sql}${sql.endsWith(' ') || sql === '' ? '' : ' '}{{cell_${ref}}}`;

/** newCellId returns an id that is unique within the notebook. */
export const newCellId = (cells: CellLike[]): string => {
  // The first FREE cN, not "length+1": after deleting the middle cell, length+1 can
  // collide with an id that is still in use.
  const taken = new Set(cells.map(c => c.id));
  let n = 1;
  while (taken.has(`c${n}`)) n += 1;
  return `c${n}`;
};

/** moveCell reorders cells, returning a new array (indexes are 0-based). */
export const moveCell = <T>(cells: T[], from: number, to: number): T[] => {
  if (from === to || from < 0 || to < 0 || from >= cells.length || to >= cells.length) {
    return cells;
  }
  const out = cells.slice();
  const [item] = out.splice(from, 1);
  out.splice(to, 0, item);
  return out;
};
