// W-M1: the monitor's client-side helpers — pure logic over the sample ring.
//
// The panel renders an inline-SVG sparkline per host (the workspace's zero-chart-
// library discipline; chart.ts's linePath draws it). These helpers shape the ring
// into what the SVG wants, and decide what the panel SHOWS when there is nothing
// honest to plot yet.

export interface MonitorSample {
  time: string;
  value: number;
}

export interface MonitorState {
  host: string;
  tag: string;
  samples: MonitorSample[];
  dropped: number;
}

// latestValue reads the newest sample, or null when the ring is empty — a panel
// with no data says "no data" instead of plotting a zero line.
export function latestValue(s: MonitorState): number | null {
  if (!s.samples || s.samples.length === 0) return null;
  return s.samples[s.samples.length - 1].value;
}

// sparkPoints converts the ring into chart.ts's point shape: the ring index as
// the label (time labels at 15s intervals would crowd a sparkline), the value
// as plotted. Samples already arrive oldest-first.
export function sparkPoints(s: MonitorState): { label: string; value: number }[] {
  return (s.samples ?? []).map((sm, i) => ({ label: String(i + 1), value: sm.value }));
}

// sparkSummary is the one-line status the panel header shows: latest value,
// the ring's fill, and the eviction count (the bounded-memory receipt).
export function sparkSummary(s: MonitorState): string {
  const latest = latestValue(s);
  if (latest === null) return '等待采样…';
  const fill = s.samples?.length ?? 0;
  const dropped = s.dropped ?? 0;
  return `${latest.toFixed(2)} · 环 ${fill}${dropped > 0 ? ` · 驱逐 ${dropped}` : ''}`;
}
