/**
 * Virtual windowing for the data grid (Kleinberg plan K-4).
 *
 * The grid renders O(rows) DOM; the scroller only shows O(viewport). This
 * module turns a scroll position into a render window plus the two spacer
 * heights, so DataGrid renders only rows in [i0, i1] and pads the rest as
 * empty spacer rows — db-dev's mountVirtualGrid recipe, in React form.
 *
 * Contracts (mirrors db-dev's virtual_grid_test.js):
 *  - window span is independent of `total` (10k rows and 100k rows scroll the
 *    same);
 *  - window is clamped to [0, total-1] and never inverted;
 *  - the padding sum equals the unrendered rows' height, so the scroller's
 *    scrollHeight (and thus the scrollbar) stays honest.
 */

/** Default row height in px. Kept in sync with the grid's `py-0.5` cells. */
export const ROW_HEIGHT = 28;

/** Rows rendered beyond the viewport on each side (smoothing + scrollbar). */
export const OVERSCAN = 8;

export interface VisibleRange {
  /** First row index to render (inclusive), clamped to [0, total-1]. */
  i0: number;
  /** Last row index to render (inclusive), clamped to [0, total-1]. */
  i1: number;
  /** Height of the spacer ABOVE the window: i0 * ROW_HEIGHT. */
  topPad: number;
  /** Height of the spacer BELOW the window: (total-1-i1) * ROW_HEIGHT. */
  bottomPad: number;
}

/**
 * Pure window arithmetic. `headerH` is the sticky thead height; when it is 0
 * the scroller's content starts at scrollTop 0, so no offset is subtracted.
 * Non-integer scroll positions are floored — windows are row indices.
 */
export function computeVisibleRange(
  scrollTop: number,
  viewportH: number,
  headerH: number,
  total: number,
  rowH = ROW_HEIGHT,
  overscan = OVERSCAN,
): VisibleRange {
  if (total <= 0) {
    return { i0: 0, i1: -1, topPad: 0, bottomPad: 0 };
  }
  const scrollable = Math.max(0, scrollTop - headerH);
  const first = Math.floor(scrollable / rowH) - overscan;
  const last = Math.floor((scrollable + viewportH) / rowH) + overscan;
  // Clamp FIRST to the valid index range, then widen: clamping i0 before i1
  // would let an out-of-range scrollTop (stale state after a result swap)
  // produce first > total-1 and an inverted window.
  const i0 = Math.min(total - 1, Math.max(0, first));
  const i1 = Math.min(total - 1, Math.max(i0, last));
  return {
    i0,
    i1,
    topPad: i0 * rowH,
    bottomPad: (total - 1 - i1) * rowH,
  };
}
