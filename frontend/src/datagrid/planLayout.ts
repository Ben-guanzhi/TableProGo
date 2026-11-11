// W11: layout for the visual execution plan.
//
// Kept as pure functions so the geometry is testable without mounting SVG: the
// component turns these numbers into rects and paths, and the numbers are where
// the bugs live (an overflowing label, a node landing on top of its sibling, a
// connector pointing at nothing).
//
// The layout is a left-to-right tree: the ROOT is the final step (the join, the
// sort) and its operands extend to the right, which is how EXPLAIN output reads.
// Rows are stacked by depth; within a depth a node's vertical position is the
// midpoint of its children, so a parent visually contains the span it consumes.

import type { PlanNode } from '../api';

export const NODE_WIDTH = 220;
export const NODE_HEIGHT = 44;
export const H_GAP = 56;
export const V_GAP = 14;
export const PADDING = 24;

export interface PlacedNode {
  node: PlanNode;
  // Top-left of the box, in layout space (before the view's pan/zoom).
  x: number;
  y: number;
  // The parent's index in the same array, or -1 for the root.
  parent: number;
}

export interface PlanLayout {
  nodes: PlacedNode[];
  width: number;
  height: number;
  // Max depth, for the header's "N 层" summary.
  depth: number;
}

// subtreeHeight is how much vertical room a node's subtree needs: its own row or
// the sum of its children's, whichever is larger. This is what keeps siblings
// from overlapping.
const subtreeHeight = (n: PlanNode): number => {
  if (!n.children || n.children.length === 0) return NODE_HEIGHT + V_GAP;
  let sum = 0;
  for (const c of n.children) sum += subtreeHeight(c);
  return Math.max(NODE_HEIGHT + V_GAP, sum);
};

// layoutPlan places every node and returns the canvas size.
//
// Deterministic by construction: children are visited in the engine's order (the
// plan's own order carries meaning — the outer side of a join first), and every
// position is a sum of fixed sizes. Two renders of one plan are identical, which
// is what makes the SVG export usable as evidence.
export const layoutPlan = (root: PlanNode): PlanLayout => {
  const nodes: PlacedNode[] = [];
  let depth = 0;

  const place = (n: PlanNode, x: number, top: number, parent: number, level: number): void => {
    const own = subtreeHeight(n);
    const center = top + own / 2;
    const index = nodes.length;
    nodes.push({ node: n, x, y: center - NODE_HEIGHT / 2, parent });
    // Depth counts the LAYOUT's levels, not the node's own `depth` field: the
    // field comes from the parser and a hand-built tree may leave it unset, but
    // the drawing always has a definite nesting.
    if (level + 1 > depth) depth = level + 1;
    let cursor = top;
    for (const child of n.children ?? []) {
      const h = subtreeHeight(child);
      place(child, x + NODE_WIDTH + H_GAP, cursor, index, level + 1);
      cursor += h;
    }
  };

  place(root, PADDING, PADDING, -1, 0);

  let maxX = 0;
  let maxY = 0;
  for (const p of nodes) {
    maxX = Math.max(maxX, p.x + NODE_WIDTH);
    maxY = Math.max(maxY, p.y + NODE_HEIGHT);
  }
  return {
    nodes,
    width: maxX + PADDING,
    height: maxY + PADDING,
    depth,
  };
};

// connectorPath draws the elbow from a parent's right edge to a child's left
// edge, so the line leaves and enters horizontally.
export const connectorPath = (parent: PlacedNode, child: PlacedNode): string => {
  const x1 = parent.x + NODE_WIDTH;
  const y1 = parent.y + NODE_HEIGHT / 2;
  const x2 = child.x;
  const y2 = child.y + NODE_HEIGHT / 2;
  const mid = x1 + (x2 - x1) / 2;
  return `M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`;
};

// nodeTone picks the box's palette from the node's meaning: a flagged full scan
// is the thing to notice, and the root is the thing to read first.
export type NodeTone = 'root' | 'warn' | 'normal';

export const nodeTone = (n: PlanNode, isRoot: boolean): NodeTone => {
  if (n.warn) return 'warn';
  if (isRoot) return 'root';
  return 'normal';
};

// fitScale answers "how much do I zoom to fit the whole plan in the viewport".
// A single-node plan must not be blown up past 1: magnifying one box to fill the
// pane tells the user nothing.
export const fitScale = (
  layout: PlanLayout,
  viewportWidth: number,
  viewportHeight: number,
): number => {
  if (layout.width <= 0 || layout.height <= 0 || viewportWidth <= 0 || viewportHeight <= 0) {
    return 1;
  }
  const scale = Math.min(viewportWidth / layout.width, viewportHeight / layout.height);
  return Math.min(1, Math.max(0.15, scale));
};

// truncateLabel shortens a node's title to fit its box. The full text stays in
// the SVG's <title> so hovering still shows it.
export const truncateLabel = (label: string, max = 34): string =>
  label.length <= max ? label : `${label.slice(0, max - 1)}…`;

// truncateDetail is the same for the numbers line, which is smaller.
export const truncateDetail = (detail: string, max = 46): string =>
  detail.length <= max ? detail : `${detail.slice(0, max - 1)}…`;
