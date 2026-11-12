// Inline chart for a notebook cell, as SVG built by hand.
//
// Why not a chart library: the workspace's ER diagram and the plan view already draw
// SVG directly, and a notebook chart is one of two shapes (a bar row or a line).
// Adding a dependency for that would be the largest thing in the bundle for the
// smallest feature.
//
// The rules that make a chart useful rather than decorative:
//   - a chart is OFFERED, not automatic: guessing that a result "looks like" a chart
//     gets it wrong often enough that users stop trusting the panel;
//   - the axis starts at zero for bars (a truncated bar axis exaggerates), and the
//     line variant says so explicitly when it does not;
//   - a cell with no numeric column reports that instead of drawing an empty frame.

export interface ChartPoint {
  label: string;
  value: number;
}

export type ChartKind = 'bar' | 'line';

export interface ChartData {
  points: ChartPoint[];
  /** The numeric column plotted. */
  valueColumn: string;
  /** The column used as the label. */
  labelColumn: string;
  kind: ChartKind;
}

const NUMERIC = /^-?\d+(\.\d+)?$/;

/** asNumber reads a cell value the way the grid displays it. */
export const asNumber = (v: unknown): number | null => {
  if (typeof v === 'number' && Number.isFinite(v)) return v;
  if (typeof v === 'string' && NUMERIC.test(v.trim())) return Number(v.trim());
  return null;
};

/**
 * pickChart decides whether a result row set can be charted, and as what.
 *
 * Returns null when there is nothing honest to draw: fewer than two rows, or no
 * column that is numeric in every row (a partly-numeric column is a text column that
 * happens to hold numbers in some rows).
 */
export const pickChart = (
  columns: string[],
  rows: unknown[][],
  kind: ChartKind = 'bar',
): ChartData | null => {
  if (!columns || !rows || rows.length < 2) return null;
  // The first fully-numeric column is the value; the first fully-textual one is the
  // label. That is the shape of every "group by, count" result without asking.
  let valueIdx = -1;
  for (let c = 0; c < columns.length; c += 1) {
    if (rows.every(r => asNumber(r[c]) !== null)) {
      valueIdx = c;
      break;
    }
  }
  if (valueIdx < 0) return null;
  let labelIdx = -1;
  for (let c = 0; c < columns.length; c += 1) {
    if (c === valueIdx) continue;
    if (rows.every(r => asNumber(r[c]) === null)) {
      labelIdx = c;
      break;
    }
  }
  if (labelIdx < 0) {
    // No text column: use the row number, which is still readable.
    return {
      points: rows.map((r, i) => ({ label: String(i + 1), value: asNumber(r[valueIdx]) ?? 0 })),
      valueColumn: columns[valueIdx],
      labelColumn: '#',
      kind,
    };
  }
  return {
    points: rows.map(r => ({
      label: String(r[labelIdx] ?? ''),
      value: asNumber(r[valueIdx]) ?? 0,
    })),
    valueColumn: columns[valueIdx],
    labelColumn: columns[labelIdx],
    kind,
  };
};

export const CHART_WIDTH = 480;
export const CHART_HEIGHT = 180;
const PAD_LEFT = 8;
const PAD_RIGHT = 8;
const PAD_TOP = 12;
const PAD_BOTTOM = 24;

/** barRects lays out the bars; the axis starts at zero, always. */
export const barRects = (data: ChartData): { x: number; y: number; w: number; h: number; label: string; value: number }[] => {
  const inner = CHART_WIDTH - PAD_LEFT - PAD_RIGHT;
  const height = CHART_HEIGHT - PAD_TOP - PAD_BOTTOM;
  const max = Math.max(...data.points.map(p => p.value), 0);
  const min = Math.min(...data.points.map(p => p.value), 0);
  const span = max - min || 1;
  const slot = inner / data.points.length;
  const barW = Math.max(2, slot * 0.7);
  return data.points.map((p, i) => {
    const h = Math.abs((p.value / span) * height);
    // Zero sits at the bottom for an all-positive series; negative bars hang below it.
    const zeroY = PAD_TOP + (max / span) * height;
    const y = p.value >= 0 ? zeroY - h : zeroY;
    return {
      x: PAD_LEFT + i * slot + (slot - barW) / 2,
      y,
      w: barW,
      h,
      label: p.label,
      value: p.value,
    };
  });
};

/** linePath builds the polyline for the line variant. */
export const linePath = (data: ChartData): string => {
  const inner = CHART_WIDTH - PAD_LEFT - PAD_RIGHT;
  const height = CHART_HEIGHT - PAD_TOP - PAD_BOTTOM;
  const values = data.points.map(p => p.value);
  const max = Math.max(...values);
  const min = Math.min(...values);
  const span = max - min || 1;
  const step = data.points.length > 1 ? inner / (data.points.length - 1) : 0;
  return data.points
    .map((p, i) => {
      const x = PAD_LEFT + i * step;
      const y = PAD_TOP + height - ((p.value - min) / span) * height;
      return `${i === 0 ? 'M' : 'L'} ${x.toFixed(1)} ${y.toFixed(1)}`;
    })
    .join(' ');
};

/** truncateLabel keeps an axis label readable without shrinking the chart. */
export const truncateLabel = (label: string, max = 10): string =>
  label.length <= max ? label : `${label.slice(0, max - 1)}…`;
