import React, { useEffect, useReducer, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import { Connection, DatabaseFilter, KVKeyInfo } from '../types';
import { GetColumns, GetDatabases, GetKVKeyInfo, GetKVKeys, GetSchemas, GetTableStructure, GetTables, TableRef } from '../api';
import DatabaseFilterDialog from './DatabaseFilterDialog';
import SidebarMenu from './SidebarMenu';
import {
  decideTreeKey,
  isMenuNode,
  isOpenWhileFiltering,
  isVisibleWhileFiltering,
  visibleTreeRows,
} from '../datagrid/treeKeys';
import {
  loadChildren as loadChildrenFor,
  loadMoreKeys as loadMoreKeysFor,
  nodeKey,
  type Loaders,
} from '../datagrid/treeLoad';
import type { ConfirmRequest } from './ConfirmDialog';
import { copyText } from '../clipboard';
import {
  FiAlertCircle,
  FiChevronRight,
  FiDatabase,
  FiEdit2,
  FiEye,
  FiFilter,
  FiFolder,
  FiHardDrive,
  FiHash,
  FiKey,
  FiLoader,
  FiPlus,
  FiPower,
  FiRefreshCw,
  FiSearch,
  FiTable,
  FiTrash2,
} from 'react-icons/fi';

import {
  emptySidebarState,
  sidebarReducer,
  type KeyInfo,
  type TreeNode as TreeStateNode,
} from '../datagrid/sidebarTree';

type NodeKind = TreeStateNode['kind'];

// The tree node as this component uses it: the reducer's shape plus the saved
// connection it may carry. The reducer stays free of the api layer, so the
// connection is narrowed here instead of there.
interface TreeNode extends Omit<TreeStateNode, 'connection'> {
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
  onOpenKey: (key: TableRef, label: string) => void;
  onInsertText: (table: TableRef) => void;
  onNewTable: (target: NewTableTarget) => void;
  onOpenER: (ref: TableRef) => void;
  onRenameObject: (table: TableRef, newName: string) => Promise<void>;
  onDropObject: (table: TableRef) => Promise<void>;
  onTruncateObject: (table: TableRef) => Promise<void>;
  onRenameKey: (key: TableRef, newName: string) => Promise<void>;
  onDeleteKey: (key: TableRef) => Promise<void>;
  onSetKeyTTL: (key: TableRef, seconds: number) => Promise<void>;
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
        key: nodeKey.schema(node.connId, node.database as string, node.schema as string),
        kind: 'schema',
        label: node.schema,
        connId: node.connId,
        dbType: node.dbType,
        database: node.database,
        schema: node.schema,
      }
    : {
        key: nodeKey.database(node.connId, node.database as string),
        kind: 'database',
        label: node.database as string,
        connId: node.connId,
        dbType: node.dbType,
        database: node.database,
      };


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
    case 'key':
      return <FiKey size={12} className="mr-1.5 shrink-0 text-accent" />;
    case 'more':
      return <FiPlus size={12} className="mr-1.5 shrink-0 text-muted" />;
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
  onOpenKey,
  onInsertText,
  onNewTable,
  onRenameObject,
  onDropObject,
  onTruncateObject,
  onRenameKey,
  onDeleteKey,
  onSetKeyTTL,
  onOpenER,
  onConfirm,
}) => {
  const [filter, setFilter] = useState('');
  const [filterFor, setFilterFor] = useState<Connection | null>(null);
  // The tree and its context menu move together (opening a menu resets its
  // The tree and its context menu move together (opening a menu resets its
  // drafts, refreshing a branch drops its descendants), so they live in one
  // reducer in datagrid/sidebarTree.ts and every update is a named transition.
  const [tree, dispatch] = useReducer(sidebarReducer, emptySidebarState);
  const { expanded, loading, errors, menu, renameDraft, ttlDraft, keyInfo, busy: ddlBusy } = tree;
  // The reducer stores the generic node; the component works with the narrower
  // one (it carries the saved connection), so the map is narrowed on the way out.
  const children = tree.children as Record<string, TreeNode[]>;
  const menuSeq = useRef(0);
  // Thin adapters over the reducer's actions: the menu and its inline editors are
  // written from a dozen places, and a named setter reads better there than a
  // dispatch with a type tag.
  const setMenu = (anchor: { node: TreeNode; x: number; y: number } | null) =>
    anchor
      ? dispatch({ type: 'menuOpen', node: anchor.node as unknown as TreeStateNode, x: anchor.x, y: anchor.y })
      : dispatch({ type: 'menuClose' });
  const setRenameDraft = (value: string | null) => dispatch({ type: 'renameDraft', value });
  const setTtlDraft = (value: string | null) => dispatch({ type: 'ttlDraft', value });
  const setKeyInfo = (info: KVKeyInfo | null) => dispatch({ type: 'keyInfo', info: info as unknown as KeyInfo | null });
  const setDdlBusy = (value: boolean) => dispatch({ type: 'busy', busy: value });
  // Roving tabindex: the tree is one tab stop, and the arrows move the row that
  // holds it. Without this the whole navigator is unreachable from the keyboard.
  const [rovingKey, setRovingKey] = useState<string | null>(null);
  const rowRefs = useRef(new Map<string, HTMLDivElement>());

  const query = filter.trim().toLowerCase();

  // Branch loading (which call answers for a node's kind, the node key formats,
  // and Redis paging) lives in datagrid/treeLoad.ts. This file supplies the
  // catalog calls and owns the loading/error state around them.
  const loaders: Loaders = {
    databases: connId => GetDatabases(connId),
    schemas: (connId, database) => GetSchemas(connId, database),
    tables: async (connId, database, schema) =>
      ((await GetTables(connId, database, schema)) ?? []) as unknown as { name: string; type: string }[],
    columns: async (connId, database, schema, table) =>
      ((await GetColumns(connId, database, schema, table)) ?? []) as unknown as {
        name: string;
        type: string;
        isPrimaryKey: boolean;
        nullable: boolean;
      }[],
    kvKeys: (connId, database, cursor) => GetKVKeys(connId, database, cursor),
  };

  const loadMoreKeys = async (node: TreeNode) => {
    if (loading[node.key]) return;
    // The "load more" row appends to the database branch it belongs to.
    const parentKey = nodeKey.database(node.connId, node.database as string);
    dispatch({ type: 'loading', key: node.key });
    try {
      const next = await loadMoreKeysFor(
        node,
        (children[parentKey] ?? []) as TreeNode[],
        loaders,
      );
      dispatch({ type: 'childrenSet', key: parentKey, rows: next as unknown as TreeStateNode[] });
    } catch (error) {
      toast.error((error as Error).message, { duration: 8000 });
    }
    dispatch({ type: 'loading', key: node.key });
  };

  const loadChildren = async (node: TreeNode) => {
    // One transition: the retry clears the old error and shows the spinner.
    dispatch({ type: 'loading', key: node.key });
    try {
      const kids = await loadChildrenFor(node, loaders);
      dispatch({ type: 'loaded', key: node.key, rows: kids as unknown as TreeStateNode[] });
    } catch (error) {
      dispatch({ type: 'failed', key: node.key, message: (error as Error).message });
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
    // Dropping a branch invalidates both its children and the expansion flags
    // below it: one transition, so the tree never renders a stale pair.
    dispatch({ type: 'subtreeCleared', key: node.key, keys: descendantKeys(node.key) });
  };

  const refresh = (node: TreeNode) => {
    clearSubtree(node);
    dispatch({ type: 'expandedSet', key: node.key, expanded: true });
    loadChildren(node);
  };

  const closeMenu = () => {
    menuSeq.current++;
    setMenu(null);
    setRenameDraft(null);
    setTtlDraft(null);
    setKeyInfo(null);
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

  // Key actions change a live key, so they reload the database branch rather
  // than one parent table. The open value tab is updated by the app.
  const runKeyAction = async (
    node: TreeNode,
    action: 'rename' | 'delete' | 'ttl',
    arg?: string | number,
  ) => {
    if (ddlBusy) return;
    const target = tableRefOf(node);
    setDdlBusy(true);
    try {
      if (action === 'rename') await onRenameKey(target, arg as string);
      else if (action === 'delete') await onDeleteKey(target);
      else await onSetKeyTTL(target, arg as number);

      closeMenu();
      refresh(parentOfTable(node));
      toast.success(
        action === 'rename'
          ? `已改名为 ${arg}`
          : action === 'delete'
            ? `已删除键 ${node.label}`
            : Number(arg) > 0
              ? `TTL 已设为 ${arg} 秒`
              : '已设为永不过期',
      );
    } catch (error) {
      toast.error((error as Error).message, { duration: 8000 });
    } finally {
      setDdlBusy(false);
    }
  };

  const commitKeyRename = (node: TreeNode) => {
    const draft = (renameDraft ?? '').trim();
    if (!draft || ddlBusy) return;
    if (draft === node.label) {
      closeMenu();
      return;
    }
    runKeyAction(node, 'rename', draft);
  };

  const commitKeyTTL = (node: TreeNode) => {
    const draft = (ttlDraft ?? '').trim();
    if (!/^\d+$/.test(draft) || ddlBusy) {
      toast.error('TTL 请填不小于 0 的整数秒');
      return;
    }
    runKeyAction(node, 'ttl', Number(draft));
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
    const seq = ++menuSeq.current;
    setRenameDraft(null);
    setTtlDraft(null);
    setKeyInfo(null);
    setMenu({ node, x, y });
    // The menu shows the key's type and remaining TTL above its actions. It is
    // a read, so a failure leaves the menu usable and just omits the line.
    if (node.kind === 'key') {
      GetKVKeyInfo(node.connId, node.database as string, node.table as string)
        .then(info => {
          if (menuSeq.current === seq) setKeyInfo(info);
        })
        .catch(() => {});
    }
  };

  const openMenu = (node: TreeNode, event: React.MouseEvent) => {
    event.preventDefault();
    event.stopPropagation();
    openMenuAt(node, event.clientX, event.clientY);
  };

  const toggle = async (node: TreeNode) => {
    if (node.kind === 'column' || node.kind === 'key' || node.kind === 'more') return;

    if (!expanded[node.key]) {
      dispatch({ type: 'expandedSet', key: node.key, expanded: true });
      if (!children[node.key]) {
        if (node.kind === 'connection' && !connectedIds.includes(node.connId)) {
          if (node.connection) {
            await onConnect(node.connection);
          }
        }
        loadChildren(node);
      }
    } else {
      dispatch({ type: 'expandedSet', key: node.key, expanded: false });
    }
  };

  // Filtering, expansion and the painted order live in datagrid/treeKeys.ts: the
  // keyboard has to agree with what is on screen, so both read one function.
  const isVisible = (node: TreeNode): boolean => isVisibleWhileFiltering(node, children, query);
  const isOpenNode = (node: TreeNode): boolean =>
    isOpenWhileFiltering(node, children, expanded, query);
  const visibleRows = (nodes: TreeNode[]): FlatRow[] =>
    visibleTreeRows(nodes, children, expanded, query) as FlatRow[];

  const focusRow = (key: string) => {
    setRovingKey(key);
    // A row that is not the tab stop yet still takes programmatic focus, so the
    // highlight and the focus ring move in the same step.
    rowRefs.current.get(key)?.focus();
  };

  const activate = (node: TreeNode) => {
    if (node.kind === 'table' || node.kind === 'view') {
      onOpenTable(tableRefOf(node), node.label, node.kind);
    } else if (node.kind === 'key') {
      onOpenKey(tableRefOf(node), node.label);
    } else if (node.kind === 'more') {
      loadMoreKeys(node);
    } else {
      toggle(node);
    }
  };

  // decideTreeKey says what the key means; this only performs it.
  const onRowKey = (event: React.KeyboardEvent, node: TreeNode, depth: number) => {
    const action = decideTreeKey(event, node, depth, visibleRows(roots), isOpenNode(node));
    if (action.kind === 'none') return;
    event.preventDefault();
    switch (action.kind) {
      case 'focus':
        focusRow(action.key);
        break;
      case 'toggle':
        toggle(action.node);
        break;
      case 'activate':
        activate(action.node);
        break;
      case 'contextMenu': {
        if (!isMenuNode(action.node.kind)) return;
        const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
        openMenuAt(action.node, rect.left + 12, rect.bottom);
        break;
      }
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
    const hasKids = node.kind !== 'column' && node.kind !== 'key' && node.kind !== 'more';
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
        onClick={() => {
          if (node.kind === 'key') onOpenKey(tableRefOf(node), node.label);
          else if (node.kind === 'more') loadMoreKeys(node);
          else toggle(node);
        }}
        onContextMenu={e => {
          if (isMenuNode(node.kind)) openMenu(node, e);
        }}
        onDoubleClick={() => {
          if (node.kind === 'table' || node.kind === 'view') {
            onInsertText(tableRefOf(node));
          } else if (node.kind === 'key') {
            onOpenKey(tableRefOf(node), node.label);
          }
        }}
      >
        <span className="w-3.5 shrink-0 flex items-center justify-center text-muted">
          {hasKids && (
            <FiChevronRight size={11} className={isOpen ? 'transform rotate-90' : ''} />
          )}
        </span>
        <NodeIcon node={node} />
        <span
          className="text-[0.8125rem] truncate"
          title={
            node.kind === 'table' || node.kind === 'view'
              ? '单击浏览数据与结构，双击插入表名，右键改名 / Drop / 复制 DDL'
              : node.kind === 'key'
                ? '单击查看键值，双击编辑值单元格，右键设置 TTL / 重命名 / 删除'
                : node.kind === 'more'
                  ? '继续读取下一页键'
                  : undefined
          }
          onClick={e => {
            e.stopPropagation();
            if (node.kind === 'table' || node.kind === 'view') {
              onOpenTable(tableRefOf(node), node.label, node.kind);
            } else if (node.kind === 'key') {
              onOpenKey(tableRefOf(node), node.label);
            } else if (node.kind === 'more') {
              loadMoreKeys(node);
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

        {node.kind !== 'connection' && node.kind !== 'key' && node.kind !== 'more' && (
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
            node.kind !== 'column' && node.kind !== 'more' && node.key in children && (
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

  // The menu's item list (and its inline rename/TTL editors) lives in
  // SidebarMenu; this component keeps the state machine that decides which one
  // is showing.
  const renderMenu = (node: TreeNode) => (
    <SidebarMenu
      node={node}
      renameDraft={renameDraft}
      ttlDraft={ttlDraft}
      keyInfo={keyInfo as unknown as KeyInfo | null}
      busy={ddlBusy}
      onRenameDraft={setRenameDraft}
      onTtlDraft={setTtlDraft}
      onClose={closeMenu}
      onCommitRename={commitRename}
      onCommitKeyRename={commitKeyRename}
      onCommitKeyTTL={commitKeyTTL}
      onRunAction={(n, action) => runAction(n, action)}
      onRunKeyAction={(n, action) => runKeyAction(n, action)}
      onRefresh={refresh}
      onRefreshKeyParent={n => refresh(parentOfTable(n))}
      onCopyDdl={copyDdl}
      onNewTable={onNewTable}
      onOpenER={n => onOpenER(tableRefOf(n))}
      onConfirm={onConfirm}
    />
  );
  const roots: TreeNode[] = connections.map(c => ({
    key: nodeKey.connection(c.id),
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
        {renderMenu(menu.node as TreeNode)}
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
