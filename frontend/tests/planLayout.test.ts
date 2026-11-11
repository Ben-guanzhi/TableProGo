import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  H_GAP,
  NODE_HEIGHT,
  NODE_WIDTH,
  connectorPath,
  fitScale,
  layoutPlan,
  nodeTone,
  truncateDetail,
  truncateLabel,
} from '../src/datagrid/planLayout.ts';
import type { PlanNode } from '../src/api.ts';

const leaf = (label: string, extra: Partial<PlanNode> = {}): PlanNode => ({
  label,
  rows: -1,
  depth: 1,
  ...extra,
});

// A root with two leaves: the shape every join plan has.
const twoChildTree = (): PlanNode => ({
  label: 'Hash Join',
  detail: 'cost=1.00..35.50 · rows=2550',
  rows: 2550,
  depth: 0,
  children: [leaf('Seq Scan on orders', { warn: '全表扫描' }), leaf('Index Scan on customers')],
});

test('a single node lays out one box inside the padding', () => {
  const layout = layoutPlan(leaf('Seq Scan on t'));
  assert.equal(layout.nodes.length, 1);
  assert.equal(layout.depth, 1);
  assert.ok(layout.width > NODE_WIDTH);
  assert.ok(layout.height > NODE_HEIGHT);
  // The root has no parent.
  assert.equal(layout.nodes[0].parent, -1);
});

test('children sit to the right of their parent and never overlap', () => {
  const layout = layoutPlan(twoChildTree());
  assert.equal(layout.nodes.length, 3);
  const [root, first, second] = layout.nodes;
  // Left-to-right: the root is first, the operands extend right.
  assert.ok(first.x >= root.x + NODE_WIDTH + H_GAP - 1);
  assert.equal(first.x, second.x, 'siblings share a column');
  // Vertical separation: the boxes must not intersect.
  assert.ok(
    first.y + NODE_HEIGHT <= second.y,
    `sibling boxes overlap: ${first.y}+${NODE_HEIGHT} vs ${second.y}`,
  );
  // The root is centred on the span its children consume.
  const mid = (first.y + second.y + NODE_HEIGHT) / 2;
  const rootMid = root.y + NODE_HEIGHT / 2;
  assert.ok(Math.abs(mid - rootMid) < 1, `root is off-centre: ${rootMid} vs ${mid}`);
});

test('layout is deterministic across repeated calls', () => {
  // Two renders of one plan must be identical: the SVG export is used as
  // evidence, and a layout that drifts makes two exports disagree.
  const tree = twoChildTree();
  const a = layoutPlan(tree);
  const b = layoutPlan(tree);
  assert.deepEqual(
    a.nodes.map(n => [n.x, n.y, n.parent]),
    b.nodes.map(n => [n.x, n.y, n.parent]),
  );
  assert.equal(a.width, b.width);
  assert.equal(a.height, b.height);
});

test('a deep chain stays inside the canvas bounds', () => {
  let node = leaf('Scan');
  for (let i = 0; i < 5; i++) {
    node = { label: `Op ${i}`, rows: -1, depth: 0, children: [node] };
  }
  const layout = layoutPlan(node);
  assert.equal(layout.nodes.length, 6);
  for (const p of layout.nodes) {
    assert.ok(p.x >= 0 && p.y >= 0);
    assert.ok(p.x + NODE_WIDTH <= layout.width);
    assert.ok(p.y + NODE_HEIGHT <= layout.height);
  }
  // A chain six deep spans six columns.
  assert.equal(layout.depth, 6);
});

test('connector leaves the parent right edge and enters the child left edge', () => {
  const layout = layoutPlan(twoChildTree());
  const [root, first] = layout.nodes;
  const path = connectorPath(root, first);
  assert.ok(path.startsWith('M '));
  // The start is the parent's right edge, vertically centred.
  const startX = root.x + NODE_WIDTH;
  assert.ok(path.includes(`${startX} ${root.y + NODE_HEIGHT / 2}`));
  // The end is the child's left edge.
  assert.ok(path.includes(`${first.x} ${first.y + NODE_HEIGHT / 2}`));
  // A cubic curve, so the elbow is smooth rather than a hard corner.
  assert.ok(path.includes('C '));
});

test('nodeTone marks the root and the flagged scan', () => {
  const tree = twoChildTree();
  assert.equal(nodeTone(tree, true), 'root');
  assert.equal(nodeTone(tree.children![0], false), 'warn');
  assert.equal(nodeTone(tree.children![1], false), 'normal');
});

test('fitScale never magnifies past 100% and never collapses to nothing', () => {
  const layout = layoutPlan(twoChildTree());
  assert.equal(fitScale(layout, 0, 0), 1, 'a zero viewport falls back to 1');
  // A giant viewport must not blow one box up to fill the pane.
  assert.equal(fitScale(layout, 10000, 10000), 1);
  // A tiny viewport shrinks, but not below the readable floor.
  const tiny = fitScale(layout, 50, 50);
  assert.ok(tiny >= 0.15 && tiny < 1, `scale = ${tiny}`);
});

test('truncation shortens with an ellipsis and leaves short text alone', () => {
  assert.equal(truncateLabel('short'), 'short');
  const long = 'x'.repeat(80);
  const cut = truncateLabel(long, 20);
  assert.equal(cut.length, 20);
  assert.ok(cut.endsWith('…'));
  assert.equal(truncateDetail('cost=1.00'), 'cost=1.00');
  assert.ok(truncateDetail(long, 30).length === 30);
});
