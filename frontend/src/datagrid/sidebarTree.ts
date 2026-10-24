// The object tree's state machine: what is expanded, what children were read,
// which node is loading or failed, and the context menu (including its inline
// rename/TTL drafts and the key info it shows).
//
// Split out of Sidebar.tsx because these eight pieces of state always move
// together: opening a menu resets the drafts, closing it drops the key info, and
// a refreshed subtree invalidates both the children and the expanded flags below
// it. One reducer makes those transitions readable instead of eight setState
// calls scattered through the component (see tests/sidebarTree.test.ts).

export type NodeKind =
  | 'connection'
  | 'database'
  | 'schema'
  | 'table'
  | 'view'
  | 'key'
  | 'column'
  | 'more';

export interface TreeNode {
  key: string;
  kind: NodeKind;
  label: string;
  connId: string;
  dbType: string;
  database?: string;
  schema?: string;
  table?: string;
  dataType?: string;
  primaryKey?: boolean;
  nullable?: boolean;
  // The saved connection a connection node carries; left opaque so this module
  // stays free of the api layer (the component narrows it).
  connection?: unknown;
  // Only the "load more keys" row carries a SCAN cursor.
  cursor?: string;
}

// A row in the order the keyboard walks it: the node plus how deep it is.
export interface FlatRow {
  node: TreeNode;
  depth: number;
}

// Which node the menu is for, and where it is anchored.
export interface MenuAnchor {
  node: TreeNode;
  x: number;
  y: number;
}

export interface KeyInfo {
  type: string;
  ttl: number;
}

// The maps are keyed by node key. TreeNode is intentionally generic so the
// component can narrow `connection` without the reducer importing the api.
export interface SidebarState {
  expanded: Record<string, boolean>;
  children: Record<string, TreeNode[]>;
  loading: Record<string, boolean>;
  errors: Record<string, string>;
  menu: MenuAnchor | null;
  // The menu's inline editors: non-null means that editor is showing.
  renameDraft: string | null;
  ttlDraft: string | null;
  keyInfo: KeyInfo | null;
  // A DDL/key action is in flight; every destructive menu item is disabled.
  busy: boolean;
}

export const emptySidebarState: SidebarState = {
  expanded: {},
  children: {},
  loading: {},
  errors: {},
  menu: null,
  renameDraft: null,
  ttlDraft: null,
  keyInfo: null,
  busy: false,
};

export type SidebarAction =
  | { type: 'loaded'; key: string; rows: TreeNode[] }
  | { type: 'loading'; key: string }
  | { type: 'failed'; key: string; message: string }
  | { type: 'childrenSet'; key: string; rows: TreeNode[] }
  | { type: 'expandedSet'; key: string; expanded: boolean }
  | { type: 'menuOpen'; node: TreeNode; x: number; y: number }
  | { type: 'menuClose' }
  | { type: 'keyInfo'; info: KeyInfo | null }
  | { type: 'renameDraft'; value: string | null }
  | { type: 'ttlDraft'; value: string | null }
  | { type: 'busy'; busy: boolean }
  | { type: 'subtreeCleared'; key: string; keys: string[] };

// A node is "still loading" only until its result lands; the three maps are
// independent so a retry clears the error and sets loading in one transition.
export function sidebarReducer(state: SidebarState, action: SidebarAction): SidebarState {
  switch (action.type) {
    case 'loading':
      return { ...state, loading: { ...state.loading, [action.key]: true }, errors: without(state.errors, action.key) };
    case 'loaded':
      return {
        ...state,
        loading: { ...state.loading, [action.key]: false },
        errors: without(state.errors, action.key),
        children: { ...state.children, [action.key]: action.rows },
      };
    case 'failed':
      return {
        ...state,
        loading: { ...state.loading, [action.key]: false },
        errors: { ...state.errors, [action.key]: action.message },
      };
    case 'childrenSet':
      return { ...state, children: { ...state.children, [action.key]: action.rows } };
    case 'expandedSet':
      return { ...state, expanded: { ...state.expanded, [action.key]: action.expanded } };
    case 'menuOpen':
      // A new menu never inherits drafts or info from the previous one.
      return { ...state, menu: { node: action.node, x: action.x, y: action.y }, renameDraft: null, ttlDraft: null, keyInfo: null };
    case 'menuClose':
      return { ...state, menu: null, renameDraft: null, ttlDraft: null, keyInfo: null };
    case 'keyInfo':
      return { ...state, keyInfo: action.info };
    case 'renameDraft':
      return { ...state, renameDraft: action.value };
    case 'ttlDraft':
      return { ...state, ttlDraft: action.value };
    case 'busy':
      return { ...state, busy: action.busy };

    case 'subtreeCleared': {
      // A refreshed branch invalidates everything read below it: drop the
      // children and collapse the descendants, else the tree shows stale rows.
      const children = { ...state.children };
      const expanded = { ...state.expanded };
      delete children[action.key];
      delete expanded[action.key];
      for (const key of action.keys) {
        delete children[key];
        delete expanded[key];
      }
      return { ...state, children, expanded };
    }
    default:
      return state;
  }
}

function without(map: Record<string, string>, key: string): Record<string, string> {
  if (!(key in map)) return map;
  const next = { ...map };
  delete next[key];
  return next;
}

// flushRows walks the tree in display order: expanded nodes contribute their
// children, collapsed ones do not. Extracted because the keyboard cursor is
// computed from it (and tested against it).
export function flushRows(nodes: TreeNode[], children: Record<string, TreeNode[]>, expanded: Record<string, boolean>, depth = 0, out: FlatRow[] = []): FlatRow[] {
  for (const node of nodes) {
    out.push({ node, depth });
    if (expanded[node.key] && children[node.key]) {
      flushRows(children[node.key], children, expanded, depth + 1, out);
    }
  }
  return out;
}

// descendantKeys returns the keys of every node below prefix that the state
// knows about, so clearing a branch can drop all of them at once.
export function descendantKeys(children: Record<string, TreeNode[]>, prefix: string): string[] {
  const out: string[] = [];
  const walk = (key: string) => {
    for (const child of children[key] ?? []) {
      out.push(child.key);
      walk(child.key);
    }
  };
  walk(prefix);
  return out;
}
