import React, { useEffect, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import { Connection, DatabaseFilter, TableInfo, ColumnInfo } from '../types';
import { GetColumns, GetDatabases, GetSchemas, GetTableStructure, GetTables, TableRef } from '../api';
import DatabaseFilterDialog from './DatabaseFilterDialog';
import type { ConfirmRequest } from './ConfirmDialog';
import { copyText } from '../clipboard';
import {
  FiAlertCircle,
  FiChevronRight,
  FiCopy,
  FiDatabase,
  FiEdit2,
  FiEye,
  FiFilter,
  FiFolder,
  FiHardDrive,
  FiHash,
  FiKey,
  FiLoader,
  FiMinusCircle,
  FiPlus,
  FiPower,
  FiRefreshCw,
  FiSearch,
  FiTable,
  FiTrash2,
} from 'react-icons/fi';

type NodeKind = 'connection' | 'database' | 'schema' | 'table' | 'view' | 'column';

interface TreeNode {
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
  connection?: Connection;
}

// A row in the order the keyboard walks it: the node plus how deep it is.
interface FlatRow {
  node: TreeNode;
  depth: number;
}

// Where "新建表" starts: the database (and schema) the user was looking at.
export interface NewTableTarget {
  connId: string;
  dbType: string;
  database: string;
  schema: string;
}

interface SidebarProps {
  connections: Connection[];
  activeConnection: Connection | null;
  connectedIds: string[];
  onConnect: (connection: Connection) => Promise<void>;
  onDisconnect: (connId: string) => void;
  onNewConnection: () => void;
  onEditConnection: (connection: Connection) => void;
  onDeleteConnection: (id: string) => void;
  onSaveDatabaseFilter: (connection: Connection, filter: DatabaseFilter) => Promise<void>;
  onOpenTable: (table: TableRef, label: string, objectType: 'table' | 'view') => void;
  onInsertText: (table: TableRef) => void;
  onNewTable: (target: NewTableTarget) => void;
  onRenameObject: (table: TableRef, newName: string) => Promise<void>;
  onDropObject: (table: TableRef) => Promise<void>;
  onTruncateObject: (table: TableRef) => Promise<void>;
  // Drop / truncate / delete-connection ask through the app's themed dialog, so
  // the same warning style covers every way to lose something.
  onConfirm: (request: ConfirmRequest) => void;
}

const tableRefOf = (node: TreeNode): TableRef => ({
  connId: node.connId,
  database: node.database as string,
  schema: node.schema ?? '',
  table: node.table as string,
});

// The branch a table sits in, rebuilt from its own names: after DDL the child
// list the tree is holding is the one thing known to be wrong.
const parentOfTable = (node: TreeNode): TreeNode =>
  node.schema
    ? {
        key: `s|${node.connId}|${node.database}|${node.schema}`,
        kind: 'schema',
        label: node.schema,
        connId: node.connId,
        dbType: node.dbType,
        database: node.database,
        schema: node.schema,
      }
    : {
        key: `d|${node.connId}|${node.database}`,
        kind: 'database',
        label: node.database as string,
        connId: node.connId,
        dbType: node.dbType,
        database: node.database,
      };

const menuNode = (kind: NodeKind) =>
  kind === 'table' || kind === 'view' || kind === 'database' || kind === 'schema';

const MenuItem: React.FC<{
  label: string;
  icon?: React.ReactNode;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}> = ({ label, icon, danger, disabled, onClick }) => (
  <button
    className={`w-full flex items-center gap-2 px-2.5 py-1 text-left hover:bg-raised disabled:opacity-40 ${
      danger ? 'text-danger' : 'text-ink'
    }`}
    disabled={disabled}
    onClick={onClick}
  >
    <span className="w-3.5 shrink-0 flex items-center justify-center">{icon}</span>
    <span className="truncate">{label}</span>
  </button>
);

// Listing any pattern is what turns the filter on, so the badge follows the
// patterns rather than a separate switch.
const filterActive = (connection?: Connection) => {
  const patterns = (list?: string[]) => (list ?? []).length;
  const filter = connection?.databaseFilter;
  return !!filter && (patterns(filter.include) > 0 || patterns(filter.exclude) > 0);
};

const NodeIcon: React.FC<{ node: TreeNode }> = ({ node }) => {
  switch (node.kind) {
    case 'connection':
      return <FiDatabase size={13} className="mr-1.5 shrink-0 text-accent" />;
    case 'database':
      return <FiHardDrive size={13} className="mr-1.5 shrink-0 text-icon-db" />;
    case 'schema':
      return <FiFolder size={13} className="mr-1.5 shrink-0 text-warning" />;
    case 'view':
      return <FiEye size={13} className="mr-1.5 shrink-0 text-icon-view" />;
    case 'table':
      return <FiTable size={13} className="mr-1.5 shrink-0 text-success" />;
    case 'column':
      return node.primaryKey
        ? <FiKey size={12} className="mr-1.5 shrink-0 text-warning" />
        : <FiHash size={11} className="mr-1.5 shrink-0 text-muted" />;
  }
};

const Sidebar: React.FC<SidebarProps> = ({
  connections,
  activeConnection,
  connectedIds,
  onConnect,
  onDisconnect,
  onNewConnection,
  onEditConnection,
  onDeleteConnection,
  onSaveDatabaseFilter,
  onOpenTable,
  onInsertText,
  onNewTable,
  onRenameObject,
  onDropObject,
  onTruncateObject,
  onConfirm,
}) => {
  const [filter, setFilter] = useState('');
  const [filterFor, setFilterFor] = useState<Connection | null>(null);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [children, setChildren] = useState<Record<string, TreeNode[]>>({});
  const [loading, setLoading] = useState<Record<string, boolean>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [menu, setMenu] = useState<{ node: TreeNode; x: number; y: number } | null>(null);
  // Not null while the menu is showing its rename field instead of its items.
  const [renameDraft, setRenameDraft] = useState<string | null>(null);
  const [ddlBusy, setDdlBusy] = useState(false);
  // Roving tabindex: the tree is one tab stop, and the arrows move the row that
  // holds it. Without this the whole navigator is unreachable from the keyboard.
  const [rovingKey, setRovingKey] = useState<string | null>(null);
  const rowRefs = useRef(new Map<string, HTMLDivElement>());

  const query = filter.trim().toLowerCase();

  const tableNodes = async (node: TreeNode, schema: string): Promise<TreeNode[]> => {
    const tables: TableInfo[] = (await GetTables(node.connId, node.database as string, schema)) ?? [];
    return tables.map(t => ({
      key: `${t.type === 'view' ? 'v' : 't'}|${node.connId}|${node.database}|${schema}|${t.name}`,
      kind: t.type === 'view' ? 'view' : 'table',
      label: t.name,
      connId: node.connId,
      dbType: node.dbType,
      database: node.database,
      schema,
      table: t.name,
    }));
  };

  const loadChildren = async (node: TreeNode) => {
    setLoading(l => ({ ...l, [node.key]: true }));
    setErrors(e => {
      const next = { ...e };
      delete next[node.key];
      return next;
    });

    try {
      let kids: TreeNode[] = [];
      if (node.kind === 'connection') {
        const databases: string[] = (await GetDatabases(node.connId)) ?? [];
        kids = databases
          .filter(db => !!db)
          .map(db => ({
            key: `d|${node.connId}|${db}`,
            kind: 'database' as NodeKind,
            label: db,
            connId: node.connId,
            dbType: node.dbType,
            database: db,
          }));
      } else if (node.kind === 'database') {
        const schemas: string[] = (await GetSchemas(node.connId, node.database as string)) ?? [];
        kids = schemas.length
          ? schemas.map(s => ({
              key: `s|${node.connId}|${node.database}|${s}`,
              kind: 'schema' as NodeKind,
              label: s,
              connId: node.connId,
              dbType: node.dbType,
              database: node.database,
              schema: s,
            }))
          : await tableNodes(node, '');
      } else if (node.kind === 'schema') {
        kids = await tableNodes(node, node.schema as string);
      } else {
        const columns: ColumnInfo[] = (await GetColumns(
          node.connId,
          node.database as string,
          node.schema as string,
          node.table as string,
        )) ?? [];
        kids = columns.map(c => ({
          key: `col|${node.connId}|${node.database}|${node.schema}|${node.table}|${c.name}`,
          kind: 'column' as NodeKind,
          label: c.name,
          connId: node.connId,
          dbType: node.dbType,
          database: node.database,
          schema: node.schema,
          table: node.table,
          dataType: c.type,
          primaryKey: c.isPrimaryKey,
          nullable: c.nullable,
        }));
      }
      setChildren(c => ({ ...c, [node.key]: kids }));
    } catch (error) {
      setErrors(e => ({ ...e, [node.key]: (error as Error).message }));
    } finally {
      setLoading(l => ({ ...l, [node.key]: false }));
    }
  };

  const descendantKeys = (key: string) => {
    const found: string[] = [];
    const stack = [key];
    while (stack.length) {
      for (const kid of children[stack.pop() as string] ?? []) {
        found.push(kid.key);
        stack.push(kid.key);
      }
    }
    return found;
  };

  const clearSubtree = (node: TreeNode) => {
    const stale = [node.key, ...descendantKeys(node.key)];
    setChildren(c => {
      const next = { ...c };
      stale.forEach(k => delete next[k]);
      return next;
    });
    setExpanded(e => {
      const next = { ...e };
      stale.forEach(k => delete next[k]);
      return next;
    });
  };

  const refresh = (node: TreeNode) => {
    clearSubtree(node);
    setExpanded(e => ({ ...e, [node.key]: true }));
    loadChildren(node);
  };

  const closeMenu = () => {
    setMenu(null);
    setRenameDraft(null);
  };

  // The menu is anchored to where the click happened, so anything that would
  // detach it - or move what is behind it - closes it first.
  useEffect(() => {
    if (!menu) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeMenu();
    };
    const onPointerDown = () => closeMenu();
    window.addEventListener('pointerdown', onPointerDown);
    window.addEventListener('blur', onPointerDown);
    window.addEventListener('resize', onPointerDown);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('pointerdown', onPointerDown);
      window.removeEventListener('blur', onPointerDown);
      window.removeEventListener('resize', onPointerDown);
      window.removeEventListener('keydown', onKey);
    };
  }, [menu]);

  // The backend refuses to run anything the catalog does not recognise, so the
  // only check left here is whether the user meant it.
  const runAction = async (
    node: TreeNode,
    action: 'drop' | 'truncate' | 'rename',
    newName?: string,
  ) => {
    if (ddlBusy) return;
    const target = tableRefOf(node);
    setDdlBusy(true);
    try {
      if (action === 'rename') await onRenameObject(target, newName as string);
      else if (action === 'drop') await onDropObject(target);
      else await onTruncateObject(target);

      closeMenu();
      refresh(parentOfTable(node));
      toast.success(
        action === 'rename'
          ? `已改名为 ${newName}`
          : action === 'drop'
            ? `已删除 ${node.label}`
            : `已清空 ${node.label}`,
      );
    } catch (error) {
      // Leave the menu open: the name field holds what was typed, so a refusal
      // that was only a typo can be fixed in place.
      toast.error((error as Error).message, { duration: 8000 });
    } finally {
      setDdlBusy(false);
    }
  };

  const commitRename = (node: TreeNode) => {
    const draft = (renameDraft ?? '').trim();
    if (!draft || ddlBusy) return;
    runAction(node, 'rename', draft);
  };

  const copyDdl = async (node: TreeNode) => {
    closeMenu();
    try {
      const structure = await GetTableStructure(
        node.connId,
        node.database as string,
        node.schema ?? '',
        node.table as string,
      );
      const ddl = structure?.createTable;
      if (!ddl) {
        toast.error('没有拿到建表语句');
        return;
      }
      if (await copyText(ddl)) toast.success('建表语句已复制到剪贴板');
      else toast.error('复制失败，请在结构页手动选择复制');
    } catch (error) {
      toast.error((error as Error).message, { duration: 8000 });
    }
  };

  const openMenuAt = (node: TreeNode, x: number, y: number) => {
    setRenameDraft(null);
    setMenu({ node, x, y });
  };

  const openMenu = (node: TreeNode, event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    openMenuAt(node, event.clientX, event.clientY);
  };

  const toggle = async (node: TreeNode) => {
    if (node.kind === 'column') return;

    if (!expanded[node.key]) {
      setExpanded(e => ({ ...e, [node.key]: true }));
      if (!children[node.key]) {
        if (node.kind === 'connection' && !connectedIds.includes(node.connId)) {
          if (node.connection) {
            await onConnect(node.connection);
          }
        }
        loadChildren(node);
      }
    } else {
      setExpanded(e => ({ ...e, [node.key]: false }));
    }
  };

  const isVisible = (node: TreeNode): boolean => {
    if (!query) return true;
    if (node.label.toLowerCase().includes(query)) return true;
    return (children[node.key] ?? []).some(isVisible);
  };

  // While filtering, keep the branches that lead to a match open; everything
  // else still follows the user's own expand/collapse state.
  const isOpenNode = (node: TreeNode): boolean => {
    if (query && (children[node.key] ?? []).some(isVisible)) return true;
    return !!expanded[node.key];
  };

  // The rows a keyboard user can actually walk, in the order they are painted.
  const visibleRows = (nodes: TreeNode[], depth: number, out: FlatRow[] = []): FlatRow[] => {
    for (const node of nodes.filter(isVisible)) {
      out.push({ node, depth });
      if (isOpenNode(node)) visibleRows(children[node.key] ?? [], depth + 1, out);
    }
    return out;
  };

  const focusRow = (key: string) => {
    setRovingKey(key);
    // A row that is not the tab stop yet still takes programmatic focus, so the
    // highlight and the focus ring move in the same step.
    rowRefs.current.get(key)?.focus();
  };

  const activate = (node: TreeNode) => {
    if (node.kind === 'table' || node.kind === 'view') {
      onOpenTable(tableRefOf(node), node.label, node.kind);
    } else {
      toggle(node);
    }
  };

  const onRowKey = (event: React.KeyboardEvent, node: TreeNode, depth: number) => {
    if (event.key === 'ContextMenu') {
      event.preventDefault();
      const rect = event.currentTarget.getBoundingClientRect();
      openMenuAt(node, rect.left + 12, rect.bottom);
      return;
    }
    const rows = visibleRows(roots, 0);
    const at = rows.findIndex(row => row.node.key === node.key);
    const go = (index: number) => {
      const target = rows[Math.max(0, Math.min(rows.length - 1, index))];
      if (target) focusRow(target.node.key);
    };
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        go(at + 1);
        break;
      case 'ArrowUp':
        event.preventDefault();
        go(at - 1);
        break;
      case 'Home':
        event.preventDefault();
        go(0);
        break;
      case 'End':
        event.preventDefault();
        go(rows.length - 1);
        break;
      case 'ArrowRight':
        event.preventDefault();
        if (node.kind === 'column') break;
        if (isOpenNode(node)) go(at + 1);
        else toggle(node);
        break;
      case 'ArrowLeft': {
        event.preventDefault();
        if (isOpenNode(node)) toggle(node);
        else {
          const parent = rows.slice(0, at).reverse().find(row => row.depth === depth - 1);
          if (parent) focusRow(parent.node.key);
        }
        break;
      }
      case 'Enter':
      case ' ':
        event.preventDefault();
        activate(node);
        break;
    }
  };

  // Rows are only 22px tall, so an action fills the row's height instead of
  // shrinking to its 11px glyph, and the hover-only group also reveals itself
  // for the row the keyboard is on.
  const ROW_ACTION = 'flex items-center justify-center w-[22px] h-[22px] shrink-0';
  const ROW_ACTIONS = `ml-auto flex items-center shrink-0 opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 group-aria-selected:opacity-100`;

  const rowClassName = (node: TreeNode) =>
    `group flex items-center pr-2 h-[22px] cursor-pointer select-none ${
      activeConnection?.id === node.connId && node.kind === 'connection'
        ? 'bg-accent/20'
        : 'hover:bg-raised/60'
    }`;

  const renderRow = (node: TreeNode, depth: number) => {
    const isLoading = !!loading[node.key];
    const isOpen = isOpenNode(node);
    const indent = 6 + depth * 13;
    const hasKids = node.kind !== 'column';
    const isTabStop = (rovingKey ?? roots[0]?.key) === node.key;

    return (
      <div
        key={node.key}
        ref={el => {
          if (el) rowRefs.current.set(node.key, el);
          else rowRefs.current.delete(node.key);
        }}
        role="treeitem"
        data-treekey={node.key}
        aria-level={depth + 1}
        aria-selected={isTabStop}
        aria-expanded={hasKids ? isOpen : undefined}
        tabIndex={isTabStop ? 0 : -1}
        onKeyDown={e => onRowKey(e, node, depth)}
        onFocus={() => setRovingKey(node.key)}
        className={rowClassName(node)}
        style={{ paddingLeft: indent }}
        onClick={() => toggle(node)}
        onContextMenu={e => {
          if (menuNode(node.kind)) openMenu(node, e);
        }}
        onDoubleClick={() => {
          if (node.kind === 'table' || node.kind === 'view') {
            onInsertText(tableRefOf(node));
          }
        }}
      >
        <span className="w-3.5 shrink-0 flex items-center justify-center text-muted">
          {node.kind !== 'column' && (
            <FiChevronRight size={11} className={isOpen ? 'transform rotate-90' : ''} />
          )}
        </span>
        <NodeIcon node={node} />
        <span
          className="text-[0.8125rem] truncate"
          title={
            node.kind === 'table' || node.kind === 'view'
              ? '单击浏览数据与结构，双击插入表名，右键改名 / Drop / 复制 DDL'
              : undefined
          }
          onClick={e => {
            e.stopPropagation();
            if (node.kind === 'table' || node.kind === 'view') {
              onOpenTable(tableRefOf(node), node.label, node.kind);
            } else if (node.kind !== 'column') {
              toggle(node);
            }
          }}
        >
          {node.label}
        </span>

        {node.kind === 'column' && node.dataType && (
          <span className="ml-auto pl-2 text-2xs text-muted truncate shrink-0">
            {node.dataType}
          </span>
        )}

        {node.kind === 'connection' && filterActive(node.connection) && (
          <FiFilter size={10} className="ml-1.5 shrink-0 text-accent" title="Database filter is on" />
        )}

        {isLoading && <FiLoader size={11} className="ml-auto animate-spin text-secondary shrink-0" />}

        {node.kind === 'connection' && (
          <span className={ROW_ACTIONS}>
            <button
              className={`${ROW_ACTION} text-secondary hover:text-accent`}
              title="Filter databases"
              aria-label="筛选这个连接的数据库"
              onClick={e => {
                e.stopPropagation();
                if (node.connection) setFilterFor(node.connection);
              }}
            >
              <FiFilter size={11} />
            </button>
            <button
              className={`${ROW_ACTION} text-secondary hover:text-ink`}
              title="Refresh"
              aria-label="重新载入这个连接的对象"
              onClick={e => {
                e.stopPropagation();
                refresh(node);
              }}
            >
              <FiRefreshCw size={11} />
            </button>
            {connectedIds.includes(node.connId) && (
              <button
                className={`${ROW_ACTION} text-secondary hover:text-warning`}
                title="Disconnect"
                aria-label="断开这个连接"
                onClick={e => {
                  e.stopPropagation();
                  clearSubtree(node);
                  onDisconnect(node.connId);
                }}
              >
                <FiPower size={11} />
              </button>
            )}
            <button
              className={`${ROW_ACTION} text-secondary hover:text-accent`}
              title="Edit"
              aria-label="编辑这个连接"
              onClick={e => {
                e.stopPropagation();
                if (node.connection) onEditConnection(node.connection);
              }}
            >
              <FiEdit2 size={11} />
            </button>
            <button
              className={`${ROW_ACTION} text-secondary hover:text-danger`}
              title="Delete"
              aria-label="删除这个连接"
              onClick={e => {
                e.stopPropagation();
                onConfirm({
                  title: `删除连接 ${node.label}`,
                  message: '只是从侧栏移除这个连接，数据库本身不受影响。',
                  confirmLabel: '删除连接',
                  destructive: true,
                  onConfirm: () => {
                    clearSubtree(node);
                    onDeleteConnection(node.connId);
                  },
                });
              }}
            >
              <FiTrash2 size={11} />
            </button>
          </span>
        )}

        {node.kind !== 'connection' && (
          <button
            className={`ml-auto ${ROW_ACTION} text-secondary hover:text-ink opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 group-aria-selected:opacity-100`}
            title="Refresh"
            aria-label="重新载入这个节点"
            onClick={e => {
              e.stopPropagation();
              refresh(node);
            }}
          >
            <FiRefreshCw size={11} />
          </button>
        )}
      </div>
    );
  };

  const renderNodes = (nodes: TreeNode[], depth: number): React.ReactNode =>
    nodes.filter(isVisible).map(node => {
      const isOpen = isOpenNode(node);
      const kids = children[node.key] ?? [];
      const error = errors[node.key];
      return (
        <React.Fragment key={node.key}>
          {renderRow(node, depth)}
          {isOpen && error && (
            <div
              role="presentation"
              className="flex items-start text-2xs text-danger py-0.5 pr-2 cursor-pointer hover:bg-danger/12"
              style={{ paddingLeft: 6 + (depth + 1) * 13 }}
              title={error}
              onClick={() => loadChildren(node)}
            >
              <FiAlertCircle size={11} className="mr-1 mt-0.5 shrink-0" />
              <span className="break-all">{error}</span>
            </div>
          )}
          {isOpen && kids.length > 0 && renderNodes(kids, depth + 1)}
          {isOpen && !error && kids.length === 0 && !loading[node.key] &&
            node.kind !== 'column' && node.key in children && (
            <div
              role="presentation"
              className="text-2xs text-muted py-0.5"
              style={{ paddingLeft: 6 + (depth + 1) * 13 }}
            >
              empty
            </div>
          )}
        </React.Fragment>
      );
    });

  const objectMenu = (node: TreeNode) => {
    const object = node.kind === 'table' || node.kind === 'view';
    const where = [node.database, node.schema, node.label].filter(Boolean).join('.');

    if (renameDraft !== null) {
      return (
        <div className="px-2.5 py-2">
          <div className="text-2xs text-secondary mb-1 truncate" title={where}>
            {node.label} 改名为
          </div>
          <input
            autoFocus
            aria-label="新名字"
            value={renameDraft}
            placeholder="只填新名字"
            onChange={e => setRenameDraft(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Enter') commitRename(node);
            }}
            className="w-full bg-canvas border border-accent rounded px-1.5 py-0.5 text-[0.75rem] text-ink focus:outline-none"
          />
          <div className="flex items-center justify-end gap-1 mt-1.5">
            <button
              className="px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
              onClick={closeMenu}
            >
              取消
            </button>
            <button
              className="px-2 py-0.5 rounded text-2xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              disabled={!renameDraft.trim() || ddlBusy}
              onClick={() => commitRename(node)}
            >
              {ddlBusy ? '执行中…' : '改名'}
            </button>
          </div>
        </div>
      );
    }

    return (
      <>
        <div className="px-2.5 pb-1 text-2xs text-muted truncate border-b border-line-soft" title={where}>
          {where}
        </div>
        {(node.kind === 'database' || node.kind === 'schema') && (
          <MenuItem
            label="新建表"
            icon={<FiPlus size={11} />}
            onClick={() => {
              closeMenu();
              onNewTable({
                connId: node.connId,
                dbType: node.dbType,
                database: node.database as string,
                schema: node.schema ?? '',
              });
            }}
          />
        )}
        {object && (
          <MenuItem
            label="复制 DDL"
            icon={<FiCopy size={11} />}
            onClick={() => copyDdl(node)}
          />
        )}
        {object && (
          <MenuItem
            label="重命名…"
            icon={<FiEdit2 size={11} />}
            disabled={ddlBusy}
            onClick={() => {
              setRenameDraft('');
            }}
          />
        )}
        {node.kind === 'table' && (
          <MenuItem
            label="清空数据"
            icon={<FiMinusCircle size={11} />}
            disabled={ddlBusy}
            onClick={() => {
              closeMenu();
              onConfirm({
                title: `清空 ${where}`,
                message: '表还在，行全部删除，这一步撤不回来。',
                confirmLabel: '清空数据',
                destructive: true,
                onConfirm: () => runAction(node, 'truncate'),
              });
            }}
          />
        )}
        {object && (
          <MenuItem
            label="Drop"
            icon={<FiTrash2 size={11} />}
            danger
            disabled={ddlBusy}
            onClick={() => {
              closeMenu();
              onConfirm({
                title: `删除 ${where}`,
                message: '结构和数据一起消失，撤不回来。',
                confirmLabel: 'Drop',
                destructive: true,
                onConfirm: () => runAction(node, 'drop'),
              });
            }}
          />
        )}
        <div className="my-1 border-t border-line-soft" />
        <MenuItem
          label="刷新"
          icon={<FiRefreshCw size={11} />}
          onClick={() => {
            closeMenu();
            refresh(node);
          }}
        />
      </>
    );
  };

  const roots: TreeNode[] = connections.map(c => ({
    key: `c|${c.id}`,
    kind: 'connection',
    label: c.name,
    connId: c.id,
    dbType: c.type,
    connection: c,
  }));

  return (
    <>
    <div className="w-72 bg-panel border-r border-line flex flex-col min-h-0">
      <div className="p-2.5 border-b border-line flex items-center justify-between">
        <span className="text-xs font-semibold uppercase tracking-wide text-secondary">Navigator</span>
        <button className="text-secondary hover:text-ink p-1" onClick={onNewConnection} title="New connection" aria-label="新建连接">
          <FiPlus size={14} />
        </button>
      </div>

      <div className="p-2 border-b border-line">
        <div className="relative">
          <FiSearch className="absolute left-2 top-1/2 -translate-y-1/2 text-secondary" size={12} />
          <input
            type="text"
            className="w-full pl-7 pr-2 py-1.5 text-xs bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
            placeholder="Filter loaded objects"
            aria-label="筛选已加载的对象"
            value={filter}
            onChange={e => setFilter(e.target.value)}
          />
        </div>
      </div>

      <div role="tree" aria-label="数据库对象" className="flex-1 overflow-y-auto py-1">
        {renderNodes(roots, 0)}

        {roots.length === 0 && (
          <div className="text-center text-muted text-xs py-3">No connections yet</div>
        )}
      </div>
    </div>

    {menu && (
      <div
        className="fixed z-50 w-[208px] bg-panel border border-line-strong rounded-md shadow-xl py-1 text-[0.75rem]"
        style={{
          left: Math.max(4, Math.min(menu.x, window.innerWidth - 220)),
          top: Math.max(4, Math.min(menu.y, window.innerHeight - 240)),
        }}
        onPointerDown={e => e.stopPropagation()}
        onContextMenu={e => {
          e.preventDefault();
          e.stopPropagation();
        }}
      >
        {objectMenu(menu.node)}
      </div>
    )}

    {filterFor && (
      <DatabaseFilterDialog
        connection={filterFor}
        onClose={() => setFilterFor(null)}
        onSave={async f => {
          await onSaveDatabaseFilter(filterFor, f);
          const connection = filterFor;
          setFilterFor(null);
          const node = roots.find(n => n.connId === connection.id);
          if (!node) return;
          if (connectedIds.includes(connection.id)) refresh(node);
          else clearSubtree(node);
        }}
      />
    )}
    </>
  );
};

export default Sidebar;
