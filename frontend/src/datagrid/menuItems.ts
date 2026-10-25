import type { TreeNode } from './sidebarTree';

// Which context-menu items a node gets. Kept as data (not JSX) so the rule is
// testable without a renderer: the menu component only maps these ids onto
// labels and handlers (see components/SidebarMenu.tsx and tests/sidebarMenu.test.ts).

export type MenuItemId =
  | 'newTable'
  | 'er'
  | 'copyDdl'
  | 'rename'
  | 'truncate'
  | 'drop'
  | 'refresh'
  | 'keyTtl'
  | 'keyRename'
  | 'keyDelete'
  | 'keyRefresh'
  | 'backup';

// backupableEngine names the two engines with an official single-file dump tool.
const backupableEngine = (dbType: string) => dbType === 'postgresql' || dbType === 'mysql';

// menuItemsFor decides the item list for one node. The three "kinds" of menu are
// deliberately different:
//   - a database/schema is a container: it can gain a table;
//   - a table is rows: it can be emptied, dropped, renamed, copied;
//   - a view has no rows of its own: no truncate;
//   - a Redis key is neither: TTL/rename/delete, never DDL.
export function menuItemsFor(node: TreeNode): MenuItemId[] {
  if (node.kind === 'key') {
    return ['keyTtl', 'keyRename', 'keyDelete', 'keyRefresh'];
  }
  const items: MenuItemId[] = [];
  if (node.kind === 'database' || node.kind === 'schema') {
    items.push('newTable');
    // The ER diagram reads every table in the schema, so it only makes sense for
    // a container node that actually owns tables.
    items.push('er');
    // Native-tool backup (pg_dump/mysqldump) exists for exactly two engines.
    if (backupableEngine(node.dbType)) items.push('backup');
  }
  if (node.kind === 'table' || node.kind === 'view') {
    items.push('copyDdl', 'rename');
    if (node.kind === 'table') items.push('truncate');
    items.push('drop');
  }
  items.push('refresh');
  return items;
}

// The inline editor a menu shows, if any. Rename and TTL replace the item list
// entirely rather than opening another panel.
export type MenuEditor = 'rename' | 'ttl' | null;

export function menuEditorFor(node: TreeNode, renameDraft: string | null, ttlDraft: string | null): MenuEditor {
  if (renameDraft !== null) return 'rename';
  if (ttlDraft !== null && node.kind === 'key') return 'ttl';
  return null;
}

// menuTitle is the breadcrumb the menu shows above its items.
export function menuTitle(node: TreeNode): string {
  if (node.kind === 'key') return `${node.database}.${node.label}`;
  return [node.database, node.schema, node.label].filter(Boolean).join('.');
}
