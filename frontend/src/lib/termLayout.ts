// W-T2: the terminal pane layout — a PURE function tree.
//
// The plan names the shape: "termLayout 纯函数树". Every mutation (split, close,
// rebind) returns a NEW tree; nothing mutates in place. That is what makes the
// layout testable without React and serializable without adapters, and it keeps
// the tree's invariant checkable in one place:
//
//   - a leaf is a terminal panel bound to at most one channel;
//   - a split has ≥2 children, each of which is a subtree in the SAME direction
//     discipline (a split's children may be leaves or perpendicular splits);
//   - closing a leaf collapses a parent with one child left (a split with one
//     child is not a split).
//
// What the tree deliberately does NOT hold: focus (independent UI state — the
// focused pane survives layout edits only as long as its leaf does), colors, or
// sizes beyond equal flex weights (a resize handle is W-T3 polish).

export type PaneDir = 'h' | 'v';

export interface PaneLeaf {
  kind: 'leaf';
  id: string;
  // The channel this pane renders; null = not connected yet (shows a connect
  // form). A leaf may exist before its channel opens — the LAYOUT is user
  // intent, the CONNECTION is runtime state.
  channelId: string | null;
}

export interface PaneSplit {
  kind: 'split';
  dir: PaneDir;
  children: PaneTree[];
}

export type PaneTree = PaneLeaf | PaneSplit;

export function isLeaf(t: PaneTree): t is PaneLeaf {
  return t.kind === 'leaf';
}

// newLeaf builds a leaf with a fresh, collision-free id. The id space is the
// CALLER's: the tree never generates ids itself (pure functions), so the
// component owns the counter and the tree only checks uniqueness.
export function newLeaf(id: string, channelId: string | null = null): PaneLeaf {
  return { kind: 'leaf', id, channelId };
}

// flatten returns the leaves in render order (depth-first, left→right). The
// order is the observable layout — a test asserts on it, not on tree shape.
export function flatten(tree: PaneTree): PaneLeaf[] {
  if (isLeaf(tree)) return [tree];
  const out: PaneLeaf[] = [];
  for (const c of tree.children) out.push(...flatten(c));
  return out;
}

export function countLeaves(tree: PaneTree): number {
  return flatten(tree).length;
}

// findLeaf returns the leaf with the given id, or null.
export function findLeaf(tree: PaneTree, leafId: string): PaneLeaf | null {
  if (isLeaf(tree)) return tree.id === leafId ? tree : null;
  for (const c of tree.children) {
    const hit = findLeaf(c, leafId);
    if (hit) return hit;
  }
  return null;
}

// splitPane divides the leaf `leafId` into two siblings side by side (dir h) or
// stacked (dir v). The NEW leaf gets `newId` and no channel — the user connects
// it (or broadcasts into it) afterwards. Splitting an unknown leaf returns the
// tree UNCHANGED: the layout is user intent, and a stale id (a pane the user
// already closed in another view) must not corrupt the tree.
export function splitPane(
  tree: PaneTree,
  leafId: string,
  dir: PaneDir,
  newId: string,
): PaneTree {
  if (isLeaf(tree)) {
    if (tree.id !== leafId) return tree;
    // The NEW leaf starts unconnected: splitting is a layout gesture, and the
    // user connects the new pane (or broadcasts into it) afterwards.
    return { kind: 'split', dir, children: [tree, newLeaf(newId)] };
  }
  return { ...tree, children: tree.children.map(c => splitPane(c, leafId, dir, newId)) };
}

// closePane removes the leaf and collapses any split left with a single child.
// Closing the LAST leaf returns an empty-ish tree — a single fresh leaf with no
// channel, because a terminal panel with no panes is not a panel.
export function closePane(tree: PaneTree, leafId: string): PaneTree {
  if (isLeaf(tree)) {
    return tree; // handled by the caller (the root leaf case is below)
  }
  const children = tree.children
    .map(c => closePane(c, leafId))
    .filter(c => !(isLeaf(c) && c.id === leafId));
  if (children.length === 0) {
    // Every child was the target (impossible for distinct ids, but a corrupt
    // tree must not loop) — fall back to a fresh empty leaf.
    return newLeaf(leafId + '-reborn');
  }
  if (children.length === 1) {
    return children[0];
  }
  return { ...tree, children };
}

// closePaneRoot handles the root-leaf case: closing the only leaf yields one
// fresh empty leaf (the panel stays alive, the session is gone).
export function closePaneRoot(tree: PaneTree, leafId: string): PaneTree {
  if (isLeaf(tree)) {
    if (tree.id !== leafId) return tree;
    return newLeaf(leafId + '-reborn');
  }
  return closePane(tree, leafId);
}

// setLeafChannel rebinds a leaf's channel (open → assign; close → null).
export function setLeafChannel(
  tree: PaneTree,
  leafId: string,
  channelId: string | null,
): PaneTree {
  if (isLeaf(tree)) {
    return tree.id === leafId ? { ...tree, channelId } : tree;
  }
  return { ...tree, children: tree.children.map(c => setLeafChannel(c, leafId, channelId)) };
}

// findLeafByChannel locates the pane rendering a channel (for focus-after-open).
export function findLeafByChannel(tree: PaneTree, channelId: string): PaneLeaf | null {
  const leaves = flatten(tree);
  for (const l of leaves) {
    if (l.channelId === channelId) return l;
  }
  return null;
}

// serialize/deserialize keep the layout in localStorage between sessions. The
// shape is the type; no schema versioning beyond JSON itself (a shape change
// just drops the saved layout — the tree rebuilds from scratch, which is the
// same recovery a killed plugin gets).
export function serialize(tree: PaneTree): string {
  return JSON.stringify(tree);
}

export function deserialize(raw: string): PaneTree | null {
  try {
    const t = JSON.parse(raw) as PaneTree;
    if (isLeaf(t) || (t.kind === 'split' && Array.isArray(t.children))) return t;
    return null;
  } catch {
    return null;
  }
}
