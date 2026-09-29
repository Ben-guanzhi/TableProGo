// The grid's view model: everything about turning fetched rows plus the user's
// sort, filter and pending edits into what is on screen. Kept free of React so
// the ordering and edit-mapping rules are testable (see tests/gridView.test.ts).

import type { RowChange } from './types';

export type FilterOp = 'eq' | 'ne' | 'contains' | 'null' | 'notNull';

export interface ColumnFilter {
  op: FilterOp;
  value: string;
}

export interface SortState {
  column: string;
  dir: 'asc' | 'desc';
}

export interface ColumnLike {
  name: string;
}

// Unapplied edits: row position in `rows` -> column name -> the typed value.
export type CellEdits = Record<number, Record<string, string>>;

export const cellText = (value: any): string => {
  if (value === null || value === undefined) return '';
  if (value instanceof Date) return value.toISOString();
  if (typeof value === 'object') return JSON.stringify(value);
  return String(value);
};

export const isNullValue = (value: any) => value === null || value === undefined;

// dir is +1 ascending, -1 descending. It is deliberately not applied to the
// NULL comparison: NULLs sort last either way, so the interesting values stay
// in view whatever the direction.
export const compareCells = (a: any, b: any, dir: number): number =>
  compareKeys(cellKey(a), cellKey(b), dir);

// What a sort needs from a cell, in the form worth computing once per row: two
// numbers compare as numbers, anything else as text, so both are kept. NULL is
// flagged rather than left as the empty string cellText gives it.
interface CellKey {
  isNull: boolean;
  num: number | null;
  text: string;
}

const cellKey = (value: any): CellKey => ({
  isNull: isNullValue(value),
  num: typeof value === 'number' ? value : null,
  text: cellText(value),
});

const compareKeys = (a: CellKey, b: CellKey, dir: number): number => {
  if (a.isNull || b.isNull) {
    if (a.isNull && b.isNull) return 0;
    return a.isNull ? 1 : -1;
  }
  if (a.num !== null && b.num !== null) return dir * (a.num - b.num);
  return dir * a.text.localeCompare(b.text);
};

// The test a filter turns a cell into, with the needle's case-folding already
// done: that part depends only on the filter, so a build pays for it once.
const filterTest = (filter: ColumnFilter): ((value: any) => boolean) => {
  if (filter.op === 'null') return value => isNullValue(value);
  if (filter.op === 'notNull') return value => !isNullValue(value);
  const needle = filter.value.trim().toLowerCase();
  if (!needle) return () => true;
  if (filter.op === 'contains') return value => cellText(value).toLowerCase().includes(needle);
  const wantsEqual = filter.op === 'eq';
  return value => (cellText(value).toLowerCase() === needle) === wantsEqual;
};

export const matchesFilter = (value: any, filter: ColumnFilter) => filterTest(filter)(value);

// Row indices in display order. Sorting and filtering only ever reorder this
// list, so an edit stays attached to the row it was made on.
export const buildView = (
  rows: any[][],
  columns: ColumnLike[],
  filters: Record<string, ColumnFilter>,
  sort: SortState | null,
): number[] => {
  const all = rows.map((_, i) => i);
  // Where each filter reads and what it asks, resolved once for the whole build.
  // Left to the row loop both are recomputed per row: the column scan alone is a
  // factor of len(columns) in a wide result, and a name has no business being
  // looked up 20 000 times to answer a question the header already settled.
  const active = Object.entries(filters).map(([name, filter]) => {
    const index = columns.findIndex(col => col.name === name);
    // A filter on a column the result does not have can match no row, which is
    // how it read when the lookup lived in the predicate.
    return { index, test: index >= 0 ? filterTest(filter) : () => false };
  });
  const kept = active.length ? all.filter(i => active.every(f => f.test(rows[i][f.index]))) : all;
  if (!sort) return kept;
  const c = columns.findIndex(col => col.name === sort.column);
  if (c < 0) return kept;
  const dir = sort.dir === 'asc' ? 1 : -1;
  // A comparison sort makes len(kept) log len(kept) comparisons however the
  // values are read, so the work per comparison is what is left to choose: key
  // each row once and let the comparisons read cached values.
  const keys: CellKey[] = [];
  for (const i of kept) keys[i] = cellKey(rows[i][c]);
  // kept is this call's own array, so the sort can go in place.
  return kept.sort((x, y) => compareKeys(keys[x], keys[y], dir));
};

// A cell with an unapplied edit shows the typed value, highlighted, while the
// original stays available for the tooltip.
export const displayCell = (value: any, pending?: string) => {
  if (pending !== undefined) return { text: pending, isNull: false, edited: true };
  return { text: cellText(value), isNull: isNullValue(value), edited: false };
};

// Record a typed value, or drop the edit when it lands back on the original.
export const withEdit = (
  edits: CellEdits,
  row: number,
  column: string,
  original: string,
  value: string,
): CellEdits => {
  const next = { ...edits };
  const cellEdits = { ...(next[row] ?? {}) };
  if (value === original) delete cellEdits[column];
  else cellEdits[column] = value;
  if (Object.keys(cellEdits).length) next[row] = cellEdits;
  else delete next[row];
  return next;
};

export const countEdits = (edits: CellEdits) =>
  Object.values(edits).reduce((total, cellEdits) => total + Object.keys(cellEdits).length, 0);

// A row the grid invented: it has no original values, and only the cells the
// user filled in are sent, so an auto-increment key and column defaults stay
// with the database. `id` is stable while the queue lives, so typing in one new
// row can never renumber another.
export interface NewRow {
  id: number;
  values: Record<string, string>;
}

// Everything waiting to be written, in three kinds. Rows are addressed by their
// position in the fetched `rows`, so sorting or filtering a view cannot move a
// change onto somebody else's row.
export interface ChangeQueue {
  updates: CellEdits;
  inserts: NewRow[];
  deletes: number[];
}

export const emptyQueue = (): ChangeQueue => ({ updates: {}, inserts: [], deletes: [] });

export const addRow = (
  queue: ChangeQueue,
  id: number,
  values: Record<string, string> = {},
): ChangeQueue => ({
  ...queue,
  inserts: [...queue.inserts, { id, values }],
});

// What a duplicate carries over: the source row's values, minus the key, which
// the database hands out again so the copy gets an identity of its own. A NULL
// source cell is left out rather than typed as text, so it stays "not written".
export const copyValues = (
  row: any[],
  columns: ColumnLike[],
  keyNames: string[],
): Record<string, string> => {
  const keys = new Set(keyNames.map(name => name.toLowerCase()));
  const values: Record<string, string> = {};
  columns.forEach((col, i) => {
    if (keys.has(col.name.toLowerCase())) return;
    if (isNullValue(row[i])) return;
    values[col.name] = cellText(row[i]);
  });
  return values;
};

export const dropRow = (queue: ChangeQueue, id: number): ChangeQueue => ({
  ...queue,
  inserts: queue.inserts.filter(row => row.id !== id),
});

// Leaving a new row's cell empty means "say nothing about this column", which is
// how a column keeps its default instead of being written as an empty string.
export const setRowValue = (
  queue: ChangeQueue,
  id: number,
  column: string,
  value: string,
): ChangeQueue => ({
  ...queue,
  inserts: queue.inserts.map(row => {
    if (row.id !== id) return row;
    const values = { ...row.values };
    if (value === '') delete values[column];
    else values[column] = value;
    return { ...row, values };
  }),
});

// Marking a row for deletion discards its pending cell edits: a row that is
// going away has nothing left to update. Marking it again is the undo.
export const toggleDelete = (queue: ChangeQueue, row: number): ChangeQueue => {
  const deleting = !queue.deletes.includes(row);
  const updates = { ...queue.updates };
  if (deleting) delete updates[row];
  return {
    updates,
    inserts: queue.inserts,
    deletes: deleting ? [...queue.deletes, row] : queue.deletes.filter(i => i !== row),
  };
};

export const queueCounts = (queue: ChangeQueue) => {
  const updates = countEdits(queue.updates);
  return {
    updates,
    inserts: queue.inserts.length,
    deletes: queue.deletes.length,
    total: updates + queue.inserts.length + queue.deletes.length,
  };
};

const editsOf = (values: Record<string, string>) =>
  Object.entries(values).map(([column, value]) => ({ column, value }));

// The payload the builder reads. Deletes go first and inserts last, so a delete
// that frees a unique value runs before the insert that takes it over.
export const toRowChanges = (queue: ChangeQueue, rows: any[][]): RowChange[] => [
  ...queue.deletes.map(row => ({ kind: 'delete' as const, original: rows[row] ?? [], edits: [] })),
  ...Object.entries(queue.updates).map(([row, values]) => ({
    original: rows[Number(row)] ?? [],
    edits: editsOf(values),
  })),
  ...queue.inserts.map(row => ({ kind: 'insert' as const, original: [], edits: editsOf(row.values) })),
];

// A cell only has candidates to offer when its column points at exactly one
// column of one other table: a composite key is filled across several cells, so
// no single editor could pick it. Catalog names do not have to match the case the
// result header came back with, so both sides are compared lower-cased.
export interface ForeignKeyLike {
  columns: string[];
  refColumns: string[];
}

export const foreignKeyFor = <T extends ForeignKeyLike>(fks: T[], column: string): T | null => {
  const name = column.toLowerCase();
  for (const fk of fks) {
    if (!fk.columns.some(col => col.toLowerCase() === name)) continue;
    return fk.columns.length === 1 && fk.refColumns.length === 1 ? fk : null;
  }
  return null;
};

export interface CandidateLike {
  value: string;
  label?: string;
}

// One row of the list: the value the cell stores, then whatever the referenced
// row has to say about it, so a list of ids is not read as a wall of digits.
export const describeCandidate = (candidate: CandidateLike): string =>
  candidate.label ? `${candidate.value} · ${candidate.label}` : candidate.value;

// The arrow keys walk the list and wrap, the way the quick-open box does.
export const cycleActive = (active: number, delta: number, length: number): number => {
  if (length <= 0) return -1;
  return (((active + delta) % length) + length) % length;
};

// Typing narrows the list rather than replacing it. A name is matched as well as
// the key, so a cell can be filled by looking for the row it points at.
export const filterCandidates = <T extends CandidateLike>(
  candidates: T[],
  query: string,
): T[] => {
  const needle = query.trim().toLowerCase();
  if (!needle) return candidates;
  return candidates.filter(
    candidate =>
      candidate.value.toLowerCase().includes(needle) ||
      (candidate.label ?? '').toLowerCase().includes(needle),
  );
};

// A Markdown table is the one export where a value can break the document: a pipe
// ends a column and a newline ends a row, so both get the escape Markdown allows.
// NULL and an empty cell look the same here, which is how the grid shows them too.
const markdownCell = (value: any): string =>
  cellText(value).replace(/\|/g, '\\|').replace(/\r?\n/g, '<br>');

export const toMarkdown = (columns: ColumnLike[], rows: any[][]): string => {
  const line = (cells: string[]) => `| ${cells.join(' | ')} |`;
  const header = columns.map(c => markdownCell(c.name));
  // Rows are appended rather than spread into the array literal, which would
  // copy the whole body once more before a single join.
  const lines = [line(header), line(header.map(() => '---'))];
  for (const row of rows) lines.push(line(columns.map((_, i) => markdownCell(row[i]))));
  return lines.join('\n');
};

// A CSV field has to be quoted when it holds the very characters that do the
// separating - a comma, a quote (doubled), or a newline.
const csvCell = (value: any): string => {
  const text = cellText(value);
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
};

// Like the Markdown export, each row is written out over the columns rather than
// over whatever cells it happens to hold, so a short row leaves the trailing
// fields empty instead of shifting values into the wrong column.
export const toCSV = (columns: ColumnLike[], rows: any[][]): string => {
  const line = (cells: string[]) => cells.join(',');
  const lines = [line(columns.map(c => csvCell(c.name)))];
  for (const row of rows) lines.push(line(columns.map((_, i) => csvCell(row[i]))));
  // The BOM is what makes Excel read UTF-8 column names correctly.
  return '\ufeff' + lines.join('\r\n');
};

// The JSON export keeps the types the driver sent - a number stays a number, a
// NULL stays null - so the file can be read straight back by a program.
export const toJSON = (columns: ColumnLike[], rows: any[][]): string =>
  JSON.stringify(
    rows.map(row => Object.fromEntries(columns.map((c, i) => [c.name, row[i] ?? null]))),
    null,
    2,
  );
