import type { NodeKind, TreeNode } from './sidebarTree';

// Loading one tree branch: which call answers for a node's kind, and how the
// answer becomes nodes.
//
// Kept out of Sidebar.tsx because none of it is React: it is "for this node, ask
// this, build these". The component supplies the API functions; this module
// decides the shape (keys are stable strings the tree uses for expand/collapse,
// so a changed key format silently loses state) and the Redis paging rules.

// The catalog calls a branch load needs. Injected so the rules can be tested
// without a server.
export interface Loaders {
  databases(connId: string): Promise<string[]>;
  schemas(connId: string, database: string): Promise<string[]>;
  tables(connId: string, database: string, schema: string): Promise<{ name: string; type: string }[]>;
  columns(
    connId: string,
    database: string,
    schema: string,
    table: string,
  ): Promise<{ name: string; type: string; isPrimaryKey: boolean; nullable: boolean }[]>;
  kvKeys(connId: string, database: string, cursor: string): Promise<{ keys?: string[]; cursor: string; done: boolean }>;
}

// Prefixes keep every node key unique across kinds, and stable: the expand and
// children maps are keyed by them, so changing a format resets the user's tree.
export const nodeKey = {
  connection: (connId: string) => `c|${connId}`,
  database: (connId: string, database: string) => `d|${connId}|${database}`,
  schema: (connId: string, database: string, schema: string) => `s|${connId}|${database}|${schema}`,
  // A table and a view live in a schema, so theirs carries it; a Redis key lives
  // in a logical database and has no schema, so it deliberately has no schema
  // segment (a two-part key would otherwise be indistinguishable from a table's).
  table: (connId: string, database: string, schema: string, table: string, kind: 'table' | 'view' | 'key') => {
    if (kind === 'key') return `k|${connId}|${database}|${table}`;
    return `${kind === 'view' ? 'v' : 't'}|${connId}|${database}|${schema}|${table}`;
  },
  column: (connId: string, database: string, schema: string, table: string, column: string) =>
    `col|${connId}|${database}|${schema}|${table}|${column}`,
  more: (connId: string, database: string, cursor: string) => `km|${connId}|${database}|${cursor}`,
};

// connectionNode is the root of one saved connection.
export function connectionNode(conn: { id: string; name: string; type: string }, payload?: unknown): TreeNode {
  return {
    key: nodeKey.connection(conn.id),
    kind: 'connection',
    label: conn.name,
    connId: conn.id,
    dbType: conn.type,
    // The saved connection is carried opaquely; the component narrows it.
    connection: payload,
  };
}

export function databaseNodes(
  base: { connId: string; dbType: string },
  databases: string[],
): TreeNode[] {
  return databases
    .filter(db => !!db)
    .map(db => ({
      key: nodeKey.database(base.connId, db),
      kind: 'database' as NodeKind,
      label: db,
      connId: base.connId,
      dbType: base.dbType,
      database: db,
    }));
}

export function schemaNodes(
  base: { connId: string; dbType: string; database?: string },
  schemas: string[],
): TreeNode[] {
  return schemas.map(s => ({
    key: nodeKey.schema(base.connId, base.database as string, s),
    kind: 'schema' as NodeKind,
    label: s,
    connId: base.connId,
    dbType: base.dbType,
    database: base.database,
    schema: s,
  }));
}

// tableNodes builds the object list of one schema. On Redis the same call lists
// keys, and a key is the leaf the tree opens: it holds a value, not columns. It
// keeps the `table` field so double-click and value tabs can address it without
// a second reference type.
export function tableNodes(
  base: { connId: string; dbType: string; database?: string },
  schema: string,
  tables: { name: string; type: string }[],
): TreeNode[] {
  const kv = base.dbType === 'redis';
  return tables.map(t => {
    const kind: NodeKind = kv ? 'key' : t.type === 'view' ? 'view' : 'table';
    return {
      key: nodeKey.table(base.connId, base.database as string, schema, t.name, kind as 'table' | 'view' | 'key'),
      kind,
      label: t.name,
      connId: base.connId,
      dbType: base.dbType,
      database: base.database,
      schema,
      table: t.name,
    };
  });
}

export function columnNodes(
  base: { connId: string; dbType: string; database?: string; schema?: string; table?: string },
  columns: { name: string; type: string; isPrimaryKey: boolean; nullable: boolean }[],
): TreeNode[] {
  return columns.map(c => ({
    key: nodeKey.column(
      base.connId,
      base.database as string,
      base.schema as string,
      base.table as string,
      c.name,
    ),
    kind: 'column' as NodeKind,
    label: c.name,
    connId: base.connId,
    dbType: base.dbType,
    database: base.database,
    schema: base.schema,
    table: base.table,
    dataType: c.type,
    primaryKey: c.isPrimaryKey,
    nullable: c.nullable,
  }));
}

// Redis keys are loaded one SCAN page at a time. A page can repeat a key (SCAN is
// allowed to), so appending de-duplicates and re-sorts the whole list; the "load
// more" row keeps the cursor the server handed back, and disappears once the
// scan reports it is done.
export function kvKeyNodes(
  base: { connId: string; dbType: string; database?: string },
  page: { keys?: string[]; cursor: string; done: boolean },
  previous: TreeNode[],
): TreeNode[] {
  const keys = previous.filter(n => n.kind === 'key');
  const seen = new Set(keys.map(n => n.label));
  for (const name of page.keys ?? []) {
    if (seen.has(name)) continue;
    seen.add(name);
    keys.push({
      key: nodeKey.table(base.connId, base.database as string, '', name, 'key'),
      kind: 'key',
      label: name,
      connId: base.connId,
      dbType: base.dbType,
      database: base.database,
      schema: '',
      table: name,
    });
  }
  keys.sort((a, b) => a.label.localeCompare(b.label));

  if (page.done) return keys;
  return [
    ...keys,
    {
      key: nodeKey.more(base.connId, base.database as string, page.cursor),
      kind: 'more',
      label: '加载更多键…',
      connId: base.connId,
      dbType: base.dbType,
      database: base.database,
      cursor: page.cursor,
    },
  ];
}

// loadChildren is the branch dispatch: for this node's kind, which call answers,
// and what the nodes look like. A connection lists databases; a database lists
// schemas, or objects directly when the engine has no schema layer (or is Redis);
// a schema lists objects; a table lists columns.
export async function loadChildren(node: TreeNode, loaders: Loaders): Promise<TreeNode[]> {
  switch (node.kind) {
    case 'connection':
      return databaseNodes({ connId: node.connId, dbType: node.dbType }, await loaders.databases(node.connId));
    case 'database': {
      if (node.dbType === 'redis') {
        const page = await loaders.kvKeys(node.connId, node.database as string, '');
        return kvKeyNodes({ connId: node.connId, dbType: node.dbType, database: node.database }, page, []);
      }
      const schemas = await loaders.schemas(node.connId, node.database as string);
      if (schemas.length) {
        return schemaNodes({ connId: node.connId, dbType: node.dbType, database: node.database }, schemas);
      }
      const tables = await loaders.tables(node.connId, node.database as string, '');
      return tableNodes({ connId: node.connId, dbType: node.dbType, database: node.database }, '', tables);
    }
    case 'schema': {
      const tables = await loaders.tables(node.connId, node.database as string, node.schema as string);
      return tableNodes(
        { connId: node.connId, dbType: node.dbType, database: node.database },
        node.schema as string,
        tables,
      );
    }
    case 'table':
    case 'view':
    case 'key': {
      const columns = await loaders.columns(
        node.connId,
        node.database as string,
        node.schema as string,
        node.table as string,
      );
      return columnNodes(
        {
          connId: node.connId,
          dbType: node.dbType,
          database: node.database,
          schema: node.schema,
          table: node.table,
        },
        columns,
      );
    }
    default:
      // A column is a leaf; "load more" is handled by loadMoreKeys, not here.
      return [];
  }
}

// loadMoreKeys appends the next SCAN page to the branch the "more" row belongs to
// and replaces that row with the page it fetched.
export async function loadMoreKeys(
  more: TreeNode,
  previous: TreeNode[],
  loaders: Loaders,
): Promise<TreeNode[]> {
  const page = await loaders.kvKeys(more.connId, more.database as string, more.cursor ?? '');
  return kvKeyNodes({ connId: more.connId, dbType: more.dbType, database: more.database }, page, previous);
}
