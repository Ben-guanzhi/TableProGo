import type { FlatRow, TreeNode } from './sidebarTree';

// Keyboard navigation of the tree: what each key does, given the rows that are
// on screen.
//
// This is the one part of the navigator that is genuinely tricky — the arrow keys
// have to agree with what is painted, ArrowRight expands when closed and moves
// when open, ArrowLeft collapses when open and jumps to the parent when closed —
// so it is expressed as a decision rather than as handlers (see
// tests/treeKeys.test.ts). The component still does the focusing.

export type TreeKeyAction<TNode extends TreeNode = TreeNode> =
  | { kind: 'none' }
  // Move the roving tab stop to this node key.
  | { kind: 'focus'; key: string }
  // Expand or collapse this node.
  | { kind: 'toggle'; node: TNode }
  // Open the node (a table's rows, a key's value).
  | { kind: 'activate'; node: TNode }
  | { kind: 'contextMenu'; node: TNode };

// keysThatExpandOpen reports whether a node can have children at all: a column, a
// Redis key and the "load more" row are leaves, so the arrows must not offer to
// expand them.
export const canExpand = <TNode extends TreeNode>(node: TNode): boolean =>
  node.kind !== 'column' && node.kind !== 'key' && node.kind !== 'more';

// decideTreeKey maps a key press onto one action.
//
// rows is the painted order (the same slice the keyboard walks), so "down" is
// simply the next entry. index is the position of the row the event came from;
// a row that is not in the list (stale render) falls back to index 0.
export function decideTreeKey<TNode extends TreeNode>(
  event: { key: string },
  node: TNode,
  depth: number,
  rows: { node: TNode; depth: number }[],
  isOpen: boolean,
): TreeKeyAction<TNode> {
  const at = rows.findIndex(row => row.node.key === node.key);
  const index = at < 0 ? 0 : at;
  const clamp = (i: number) => rows[Math.max(0, Math.min(rows.length - 1, i))];

  switch (event.key) {
    case 'ArrowDown': {
      const target = clamp(index + 1);
      return target ? { kind: 'focus', key: target.node.key } : { kind: 'none' };
    }
    case 'ArrowUp': {
      const target = clamp(index - 1);
      return target ? { kind: 'focus', key: target.node.key } : { kind: 'none' };
    }
    case 'Home': {
      const target = rows[0];
      return target ? { kind: 'focus', key: target.node.key } : { kind: 'none' };
    }
    case 'End': {
      const target = rows[rows.length - 1];
      return target ? { kind: 'focus', key: target.node.key } : { kind: 'none' };
    }
    case 'ArrowRight': {
      if (!canExpand(node)) return { kind: 'none' };
      // Open: step into the children. Closed: open it, so one press reveals the
      // branch instead of requiring a separate Enter.
      if (isOpen) {
        const target = clamp(index + 1);
        return target ? { kind: 'focus', key: target.node.key } : { kind: 'none' };
      }
      return { kind: 'toggle', node };
    }
    case 'ArrowLeft': {
      if (isOpen && canExpand(node)) return { kind: 'toggle', node };
      // Closed (or a leaf): go to the parent, which is the nearest earlier row one
      // level up — the painted order makes that a backwards scan.
      const parent = rows
        .slice(0, index)
        .reverse()
        .find(row => row.depth === depth - 1);
      return parent ? { kind: 'focus', key: parent.node.key } : { kind: 'none' };
    }
    case 'Enter':
    case ' ':
      return { kind: 'activate', node };
    case 'ContextMenu':
      return { kind: 'contextMenu', node };
    default:
      return { kind: 'none' };
  }
}

// isMenuNode reports whether a node kind has a context menu at all.
export const isMenuNode = (kind: TreeNode['kind']): boolean =>
  kind === 'table' || kind === 'view' || kind === 'database' || kind === 'schema' || kind === 'key';

// isVisibleWhileFiltering: a branch survives the filter when it or any descendant
// matches, so the tree can keep the path to a hit open.
export function isVisibleWhileFiltering(
  node: TreeNode,
  children: Record<string, TreeNode[]>,
  query: string,
): boolean {
  if (!query) return true;
  if (node.label.toLowerCase().includes(query)) return true;
  return (children[node.key] ?? []).some(child => isVisibleWhileFiltering(child, children, query));
}

// isOpenWhileFiltering: while filtering, a branch that leads to a hit is shown
// open regardless of the user's own expand state.
export function isOpenWhileFiltering(
  node: TreeNode,
  children: Record<string, TreeNode[]>,
  expanded: Record<string, boolean>,
  query: string,
): boolean {
  if (query && (children[node.key] ?? []).some(child => isVisibleWhileFiltering(child, children, query))) {
    return true;
  }
  return !!expanded[node.key];
}

// visibleTreeRows is the painted order: filtered nodes only, and children only
// under an open parent.
export function visibleTreeRows(
  nodes: TreeNode[],
  children: Record<string, TreeNode[]>,
  expanded: Record<string, boolean>,
  query: string,
  depth = 0,
  out: FlatRow[] = [],
): FlatRow[] {
  for (const node of nodes) {
    if (!isVisibleWhileFiltering(node, children, query)) continue;
    out.push({ node, depth });
    if (isOpenWhileFiltering(node, children, expanded, query)) {
      visibleTreeRows(children[node.key] ?? [], children, expanded, query, depth + 1, out);
    }
  }
  return out;
}
