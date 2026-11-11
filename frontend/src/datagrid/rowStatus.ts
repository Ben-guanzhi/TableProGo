// W3-b: what colour a grid row earns from the pending edit queue.
//
// Beekeeper colours staged rows green (insert) / red (delete) / orange
// (update). The point is that a user about to hit Apply can see, without
// reading a count, which rows are about to change and how. This module derives
// the colour from the SAME ChangeQueue the apply path reads — one source, so a
// row can never be painted as "new" and committed as an "update".
//
// Display-only, pure, and total: every row index answers a status, and a row
// with nothing queued answers 'clean'.

import type { ChangeQueue } from '../gridView.ts';

export type RowStatus = 'clean' | 'insert' | 'update' | 'delete';

// RowStatusMap maps a VIEW row index (the sorted/filtered order the grid shows)
// to its status. Missing keys mean 'clean'.
export type RowStatusMap = Record<number, Exclude<RowStatus, 'clean'>>;

// rowStatuses derives the per-row colour from the queue.
//
// Precedence matters: a deleted row can also carry queued updates (the user
// edited it, then marked it for deletion). Delete wins — the row is going away,
// so painting it "updated" would promise a change that will never happen. The
// same argument makes a prefixed update on a deleted row invisible, which is
// correct: nothing of that row is being saved.
export const rowStatuses = (queue: ChangeQueue): RowStatusMap => {
  const map: RowStatusMap = {};
  for (const row of queue.deletes) map[row] = 'delete';
  for (const row of Object.keys(queue.updates)) {
    const index = Number(row);
    // Number('x') is NaN; a malformed key must not become a phantom row.
    if (!Number.isInteger(index) || index < 0) continue;
    if (map[index] !== 'delete') map[index] = 'update';
  }
  return map;
};

// INSERTED rows are queued rather than indexed (they do not exist in the view
// yet), so they are reported separately as a count — the grid paints them at
// the tail, where they are rendered.
export interface StagedSummary {
  statuses: RowStatusMap;
  inserts: number;
  updates: number;
  deletes: number;
}

// stagedSummary is the one call a grid makes: colours plus the counts the
// toolbar badge shows.
export const stagedSummary = (queue: ChangeQueue): StagedSummary => {
  const statuses = rowStatuses(queue);
  let updates = 0;
  let deletes = 0;
  for (const status of Object.values(statuses)) {
    if (status === 'update') updates++;
    else if (status === 'delete') deletes++;
  }
  return { statuses, inserts: queue.inserts.length, updates, deletes };
};

// rowStatus reads one row's status, defaulting to clean. Provided so render
// code never indexes a possibly-missing key.
export const rowStatus = (statuses: RowStatusMap, row: number): RowStatus =>
  statuses[row] ?? 'clean';

// statusesOf is the memoized-by-caller entry point the grid uses: it derives the
// map once per queue change instead of once per rendered row.
export const statusesOf = (queue: ChangeQueue): RowStatusMap => rowStatuses(queue);

// statusClass is the CSS class suffix the grid applies. Keeping the mapping here
// (rather than as a ternary in JSX) lets a test assert the four cases and keeps
// the class names in one place with the palette definitions.
export const statusClass = (status: RowStatus): string => {
  switch (status) {
    case 'insert':
      return 'row-insert';
    case 'update':
      return 'row-update';
    case 'delete':
      return 'row-delete';
    default:
      return '';
  }
};

// statusLabel is the toolbar's wording for a status, used by the legend so the
// three colours are never unexplained.
export const statusLabel = (status: Exclude<RowStatus, 'clean'>): string => {
  switch (status) {
    case 'insert':
      return '新增';
    case 'update':
      return '修改';
    case 'delete':
      return '删除';
  }
};
