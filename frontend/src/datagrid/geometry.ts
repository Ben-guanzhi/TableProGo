import { useMemo } from 'react';

import { cellText } from '../gridView.ts';
import type { QueryResult } from '../types/index.ts';

// Narrow enough for dense tables, wide enough that a short column name and its
// type label both stay readable in the header.
export const MIN_WIDTH = 88;
export const MAX_WIDTH = 360;
export const MANUAL_MIN_WIDTH = 40;
export const MANUAL_MAX_WIDTH = 1600;
export const ROW_NUMBER_WIDTH = 64;

// Only this many rows are sampled for a width, so a million-row page does not
// make the first paint cost O(rows * columns).
export const SAMPLE_ROWS = 50;

// A cell is measured by its rendered text, capped so one enormous value cannot
// claim the whole viewport.
export const SAMPLE_CHARS = 46;

// Character advance used for the monospace-ish grid text, in px.
export const CHAR_WIDTH = 7;

export const computeColumnWidths = (result: QueryResult): number[] => {
  const columns = result.columns ?? [];
  const rows = result.rows ?? [];
  return columns.map((col, i) => {
    let longest = col.name.length * CHAR_WIDTH + 28;
    for (let r = 0; r < Math.min(rows.length, SAMPLE_ROWS); r++) {
      const text = cellText(rows[r][i]);
      if (text) longest = Math.max(longest, Math.min(text.length, SAMPLE_CHARS) * CHAR_WIDTH + 24);
    }
    return Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, longest));
  });
};

// Widths are sampled rather than auto-laid-out so the grid keeps a stable,
// column-first look instead of stretching to the viewport.
export const useColumnWidths = (result: QueryResult): number[] =>
  useMemo(() => computeColumnWidths(result), [result]);
