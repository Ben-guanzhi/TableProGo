import { useState, useEffect, useRef } from 'react';
import { FiEye, FiGitBranch, FiHelpCircle, FiKey, FiLoader, FiMessageCircle, FiSearch, FiShuffle, FiTable } from 'react-icons/fi';
import { Toaster } from 'react-hot-toast';
import Sidebar, { NewTableTarget } from './components/Sidebar';
import BackupDialog from './components/BackupDialog';
import RoutinesDialog from './components/RoutinesDialog';
import EnumsDialog from './components/EnumsDialog';
import StructureEditorDialog from './components/StructureEditorDialog';
import QueryEditor, { QueryEditorHandle } from './components/QueryEditor';
import DataGrid from './components/DataGrid';
import ObjectPanel, { DEFAULT_PAGE_SIZE } from './components/ObjectPanel';
import ObjectQueryPage from './components/ObjectQueryPage';
import RowSplitter, {
  clampEditorHeight,
  editorMaxHeight,
  EDITOR_MIN_HEIGHT,
} from './components/RowSplitter';
import QuickOpen from './components/QuickOpen';
import AiPanel from './components/AiPanel';
import ShortcutHelp from './components/ShortcutHelp';
import ThemePicker from './components/ThemePicker';
import ConnectionDialog from './components/ConnectionDialog';
import ConfirmDialog, { ConfirmRequest } from './components/ConfirmDialog';
import ImportDialog from './components/ImportDialog';
import CompareView from './components/CompareView';
import KvValuePanel from './components/KvValuePanel';
import ERDiagram from './components/ERDiagram';
import {
  Connection,
  ConnectionConfig,
  DatabaseFilter,
  Tab,
  QueryResult,
  SqlFilter,
  ColumnInfo,
  TableStructure,
  HistoryEntry,
  ObjectSection,
  PreviewCursor,
  KVValue,
  Column,
} from './types';
import {
  GetConnections,
  Connect,
  Disconnect,
  CancelQuery,
  SecurityNotice,
  GetDatabases,
  AddConnection,
  UpdateConnection,
  DeleteConnection,
  TestConnection,
  PreviewTable,
  RunTableAction,
  GetTableStructure,
  GetHistory,
  ClearHistory,
  QueryColumns,
  GetQuerySource,
  SearchObjects,
  GetColumns,
  GetTables,
  GetSettings,
  SaveSettings,
  SqlName,
  TableRef,
  ObjectMatch,
  AppSettings,
  GetCreateTableTemplate,
  GetKVValue,
  RenameKVKey,
  DeleteKVKey,
  SetKVKeyTTL,
} from './api';
import { applyTheme } from './theme';
import toast from 'react-hot-toast';
import { runGuarded, runStreamed } from './lib/queryRun';
import {
  BeginTransaction,
  CommitTransaction,
  ExecuteQueryInTab,
  RollbackTransaction,
} from './api';
import { defaultSettings, saveSettings, useLoadSettings, useSettingsEffect } from './lib/useSettings';

// Result columns are matched to table metadata by name, which is all a
// projection tells us once aliases and expressions get involved.
const commentMap = (columns: ColumnInfo[]) => {
  const map: Record<string, string> = {};
  for (const c of columns) {
    if (c.comment) map[c.name.toLowerCase()] = c.comment;
  }
  return map;
};

// How a table has to be spelled to resolve comes from the backend
// (SqlName → tableprosql), the one dialect table both clients share;
// the webview used to hand-build these shapes unquoted.


const objectKey = (ref: TableRef) => `${ref.connId}|${ref.database}|${ref.schema}|${ref.table}`;

const objectQualifier = (ref: TableRef, dbType?: string) => {
  if (dbType === 'mysql') return ref.database;
  if (ref.schema) return `${ref.database}.${ref.schema}`;
  return ref.database;
};

function App() {
  const [connections, setConnections] = useState<Connection[]>([]);
  const [activeConnection, setActiveConnection] = useState<Connection | null>(null);
  const [connectedIds, setConnectedIds] = useState<string[]>([]);
  const [tabs, setTabs] = useState<Tab[]>([]);
  const [activeTab, setActiveTab] = useState<string | null>(null);
  const [showConnectionDialog, setShowConnectionDialog] = useState(false);
  const [importTarget, setImportTarget] = useState<TableRef | null>(null);
  const [editingConnection, setEditingConnection] = useState<Connection | null>(null);
  const [settings, setSettings] = useState<AppSettings>(defaultSettings);
  // The pane height is a workspace preference: it survives restarts instead
  // of snapping back to 200 on every launch.
  const [editorHeight, setEditorHeightState] = useState(() => {
    try {
      const n = Number(localStorage.getItem('tablepro.editorHeight'));
      if (n >= EDITOR_MIN_HEIGHT && n <= 800) return n;
    } catch { /* fall through to the default */ }
    return 200;
  });
  const setEditorHeight = (h: number) => {
    setEditorHeightState(h);
    try {
      localStorage.setItem('tablepro.editorHeight', String(h));
    } catch { /* private mode: the choice just does not survive */ }
  };
  const [databases, setDatabases] = useState<string[]>([]);
  const [quickOpen, setQuickOpen] = useState(false);
  const [showShortcuts, setShowShortcuts] = useState(false);
  const [showAiPanel, setShowAiPanel] = useState(false);
  const [confirmReq, setConfirmReq] = useState<ConfirmRequest | null>(null);
  // Edits waiting in each tab's grid, reported by the grid itself. Kept out of
  // tab state because it changes with every keystroke in a cell.
  const pendingEdits = useRef(new Map<string, number>());
  const editorRef = useRef<QueryEditorHandle>(null);
  const objectEditorRef = useRef<QueryEditorHandle>(null);
  const tableCache = useRef(new Map<string, Promise<string[]>>());
  const columnCache = useRef(new Map<string, Promise<ColumnInfo[]>>());
  const loadedDatabases = useRef(new Map<string, string[]>());

  useEffect(() => {
    loadConnections();
  }, []);

  // Settings: load the persisted choice once, then keep the palette in sync.
  useLoadSettings(setSettings);
  useSettingsEffect(settings);

  // The tab-level database picker needs the server's list, not just the
  // connection default; sqlite has nothing to choose from.
  useEffect(() => {
    const conn = activeConnection;
    if (!conn || conn.type === 'sqlite') {
      setDatabases([]);
      return;
    }
    const cached = loadedDatabases.current.get(conn.id);
    if (cached) {
      setDatabases(cached);
      return;
    }
    let cancelled = false;
    GetDatabases(conn.id)
      .then(dbs => {
        loadedDatabases.current.set(conn.id, dbs);
        if (!cancelled) setDatabases(dbs);
      })
      .catch(error => {
        if (!cancelled) toast.error(`数据库列表加载失败：${(error as Error).message}`);
      });
    return () => {
      cancelled = true;
    };
  }, [activeConnection]);

  const tabDatabase = (tab?: Tab) => tab?.database || activeConnection?.database || '';

  const changeSettings = (next: AppSettings) => {
    setSettings(next);
    saveSettings(next);
  };

  // Cached per connection + database so completion never re-introspects on
  // every keystroke. Unqualified statements follow the tab's picker.
  const completionTables = async (database?: string): Promise<string[]> => {
    if (!activeConnection) return [];
    const db = database || tabDatabase(tabs.find(t => t.id === activeTab));
    const key = `${activeConnection.id}|${db}`;
    let pending = tableCache.current.get(key);
    if (!pending) {
      pending = GetTables(activeConnection.id, db, '')
        .then(t => (t as unknown as { name: string }[]).map(x => x.name))
        .catch(() => {
          tableCache.current.delete(key);
          return [] as string[];
        });
      tableCache.current.set(key, pending);
    }
    return pending;
  };

  const completionColumns = async (table: string, database?: string): Promise<ColumnInfo[]> => {
    if (!activeConnection) return [];
    const db = database || tabDatabase(tabs.find(t => t.id === activeTab));
    const key = `${activeConnection.id}|${db}|${table.toLowerCase()}`;
    let pending = columnCache.current.get(key);
    if (!pending) {
      pending = GetColumns(activeConnection.id, db, '', table)
        .then(c => c as unknown as ColumnInfo[])
        .catch(() => {
          columnCache.current.delete(key);
          return [] as ColumnInfo[];
        });
      columnCache.current.set(key, pending);
    }
    return pending;
  };


  const loadConnections = async () => {
    try {
      const conns = await GetConnections();
      setConnections(conns as unknown as Connection[]);
    } catch (error) {
      console.error('Failed to load connections:', error);
      toast.error('连接列表加载失败：' + (error as Error).message);
    }
  };

  const handleConnect = async (connection: Connection) => {
    try {
      await Connect(connection.id);
    } catch (error) {
      // Without this the tree just fails to expand; the user never learns why.
      toast.error(`连接失败：${(error as Error).message}`, { duration: 8000 });
      return;
    }
    toast.success(`已连接 ${connection.name}`);
    setActiveConnection(connection);
    setConnectedIds(ids => (ids.includes(connection.id) ? ids : [...ids, connection.id]));
  };

  const handleDisconnect = async (connId: string) => {
    try {
      await Disconnect(connId);
      setConnectedIds(ids => ids.filter(id => id !== connId));
      if (activeConnection?.id === connId) {
        setActiveConnection(null);
      }
      // Object tabs hold catalog reads that die with the pool, so close them
      // rather than leaving a dead grid on screen. An ER tab's metadata reads
      // the same pool, so it goes with them.
      const kept = tabs.filter(
        t => !((t.kind === 'table' || t.kind === 'kv' || t.kind === 'er') && t.tableRef?.connId === connId),
      );
      if (kept.length !== tabs.length) {
        setTabs(kept);
        if (activeTab && !kept.some(t => t.id === activeTab)) {
          setActiveTab(kept.length > 0 ? kept[0].id : null);
        }
      }
    } catch (error) {
      console.error('Failed to disconnect:', error);
      toast.error(`断开失败：${(error as Error).message}`);
    }
  };

  const handleNewTab = () => {
    const newTab: Tab = {
      id: `tab_${Date.now()}`,
      title: `Query ${tabs.length + 1}`,
      query: '',
      database: tabDatabase(),
      isDirty: false,
      connectionId: activeConnection?.id,
    };
    setTabs([...tabs, newTab]);
    setActiveTab(newTab.id);
  };

  const closeTabForReal = (tabId: string) => {
    pendingEdits.current.delete(tabId);
    const newTabs = tabs.filter(t => t.id !== tabId);
    setTabs(newTabs);
    if (activeTab === tabId) {
      setActiveTab(newTabs.length > 0 ? newTabs[0].id : null);
    }
  };

  const handleCloseTab = (tabId: string) => {
    const waiting = pendingEdits.current.get(tabId) ?? 0;
    if (waiting > 0) {
      setConfirmReq({
        title: '关闭未保存的改动',
        message: `这个网格里还有 ${waiting} 处改动没有写入数据库，关闭后它们会直接丢掉。`,
        confirmLabel: '丢弃并关闭',
        destructive: true,
        onConfirm: () => closeTabForReal(tabId),
      });
      return;
    }
    closeTabForReal(tabId);
  };

  const commentCache = useRef(new Map<string, Promise<Record<string, string>>>());

  const queryComments = async (connId: string, query: string, database: string) => {
    const key = `${connId}|${database}|${query}`;
    let pending = commentCache.current.get(key);
    if (!pending) {
      pending = QueryColumns(connId, query, database)
        .then(cols => commentMap(cols as unknown as ColumnInfo[]))
        .catch(() => {
          commentCache.current.delete(key);
          return {};
        });
      commentCache.current.set(key, pending);
    }
    return pending;
  };

  // Bumping a tab's run sequence is how "取消等待" makes the in-flight result
  // land nowhere; the server keeps running the statement either way.
  const runSeq = useRef(new Map<string, number>());

  // Interactive transactions (backend txn.go): one per tab. The status the backend
  // returns is the tab's badge; an empty status means autocommit.
  const txHandlers = (tab: Tab) => ({
    txActive: !!tab.txStatus,
    txStatus: tab.txStatus ?? '',
    onBeginTx: async () => {
      if (!activeConnection) return;
      try {
        const status = await BeginTransaction(activeConnection.id, tab.id, tabDatabase(tab));
        setTabs(current => current.map(t => (t.id === tab.id ? { ...t, txStatus: status } : t)));
      } catch (error) {
        toast.error((error as Error).message, { duration: 8000 });
      }
    },
    onCommitTx: async () => {
      if (!activeConnection) return;
      try {
        await CommitTransaction(activeConnection.id, tab.id);
        setTabs(current => current.map(t => (t.id === tab.id ? { ...t, txStatus: '' } : t)));
      } catch (error) {
        toast.error((error as Error).message, { duration: 8000 });
      }
    },
    onRollbackTx: async () => {
      if (!activeConnection) return;
      try {
        await RollbackTransaction(activeConnection.id, tab.id);
        setTabs(current => current.map(t => (t.id === tab.id ? { ...t, txStatus: '' } : t)));
      } catch (error) {
        toast.error((error as Error).message, { duration: 8000 });
      }
    },
  });

  // Every switch away from a tab goes through here: the grid's edit queue
  // dies with the component, so edits the user has not applied yet are lost.
  // The close path warns too; switching must not be the silent loophole.
  const switchToTab = (tabId: string) => {
    if (tabId === activeTab) return;
    const waiting = activeTab ? (pendingEdits.current.get(activeTab) ?? 0) : 0;
    if (waiting > 0) {
      setConfirmReq({
        title: '切换未保存的改动',
        message: `当前网格还有 ${waiting} 处改动没有写入数据库，切换标签后它们会直接丢掉。`,
        confirmLabel: '丢弃并切换',
        destructive: true,
        onConfirm: () => setActiveTab(tabId),
      });
      return;
    }
    setActiveTab(tabId);
  };

  // The keyring fallback is silent on the backend side by design; surface it
  // once per session so plain-text storage never stays invisible.
  useEffect(() => {
    SecurityNotice().then(notice => {
      if (notice) toast.error(notice, { duration: 12000 });
    }).catch(() => {});
  }, []);

  const cancelQuery = (tabId: string) => {
    runSeq.current.set(tabId, (runSeq.current.get(tabId) ?? 0) + 1);
    setTabs(current => current.map(t => (t.id === tabId ? { ...t, loading: false } : t)));
  };

  // Same idea for the object tab's query page: bump its sequence so the
  // trailing execute's result becomes a no-op, then drop the spinner.
  const cancelObjectQuery = (tabId: string) => {
    const key = `${tabId}|query`;
    objectSeq.current.set(key, (objectSeq.current.get(key) ?? 0) + 1);
    setTabs(current =>
      current.map(t => (t.id === tabId ? { ...t, objectResult: { loading: false } } : t))
    );
  };

  const handleExecuteQuery = async (tab: Tab) => {
    if (!activeConnection) return;
    const database = tabDatabase(tab);
    const tabId = tab.id;
    const seq = (runSeq.current.get(tabId) ?? 0) + 1;
    runSeq.current.set(tabId, seq);
    setTabs(current => current.map(t => (t.id === tabId ? { ...t, loading: true } : t)));
    const stillCurrent = () => (runSeq.current.get(tabId) ?? 0) === seq;

    try {
      const connId = activeConnection.id;
      // A tab with an open transaction must run through the tab-scoped route, or the
      // statement would go out in autocommit and the transaction would govern
      // nothing. Streaming is still used: the transaction and streaming are
      // independent (beginRun holds the cancel context either way).
      const result = await runStreamed(connId, tab.query, database, partial => {
        if (!stillCurrent()) return;
        setTabs(current => current.map(t =>
          t.id === tabId ? { ...t, database, result: partial, loading: true } : t
        ));
      }, tabId, !!tab.txStatus);
      const comments = settings.showColumnComments && !result.error
        ? await queryComments(activeConnection.id, tab.query, database)
        : undefined;
      // Which table, if any, the rows can be written back to; a statement that
      // reads from more than one gets null and stays read-only.
      const source = result.error ? null : await GetQuerySource(activeConnection.id, tab.query, database);
      if (!stillCurrent()) return;

      setTabs(current => current.map(t =>
        t.id === tabId
          ? { ...t, database, result, comments, source, loading: false, isDirty: false }
          : t
      ));
    } catch (error) {
      if (!stillCurrent()) return;
      const message = (error as Error).message;
      console.error('Failed to execute query:', error);
      setTabs(current => current.map(t =>
        t.id === tabId
          ? {
              ...t,
              database,
              comments: undefined,
              source: null,
              loading: false,
              result: {
                columns: [],
                rows: [],
                rowCount: 0,
                affectedRows: 0,
                lastInsertId: 0,
                executionTime: 0,
                error: message,
              },
            }
          : t
      ));
    }
  };

  // Turning the setting on should annotate what is already on screen, not only
  // the next query.
  useEffect(() => {
    if (!settings.showColumnComments || !activeConnection) return;

    const tab = tabs.find(t => t.id === activeTab);
    if (tab?.result && !tab.comments) {
      queryComments(activeConnection.id, tab.query, tabDatabase(tab)).then(comments => {
        if (Object.keys(comments).length) {
          setTabs(current => current.map(t => (t.id === tab.id ? { ...t, comments } : t)));
        }
      });
    }

    const ref = tab?.kind === 'table' ? tab.tableRef : undefined;
    if (ref && tab?.data?.data && !tab.comments) {
      GetColumns(ref.connId, ref.database, ref.schema, ref.table)
        .then(cols => {
          const comments = commentMap(cols as unknown as ColumnInfo[]);
          if (Object.keys(comments).length) {
            setTabs(current => current.map(t => (t.id === tab.id ? { ...t, comments } : t)));
          }
        })
        .catch(() => {});
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [settings.showColumnComments, activeTab, tabs]);

  // Each object tab section loads on demand, and a refresh bumps the section's
  // sequence so an earlier response cannot overwrite the newer one.
  const objectSeq = useRef(new Map<string, number>());

  const loadObjectSection = async (tab: Tab, section: ObjectSection) => {
    const ref = tab.tableRef;
    if (!ref) return;
    const key = `${tab.id}|${section}`;
    const seq = (objectSeq.current.get(key) ?? 0) + 1;
    objectSeq.current.set(key, seq);
    const stillCurrent = () => (objectSeq.current.get(key) ?? 0) === seq;
    const patch = (next: Partial<Tab>) =>
      setTabs(current => current.map(t => (t.id === tab.id ? { ...t, ...next } : t)));

    if (section === 'history') {
      patch({ history: { loading: true } });
      try {
        const entries = (await GetHistory({
          connId: ref.connId,
          database: ref.database,
          table: ref.table,
        })) as unknown as HistoryEntry[];
        if (stillCurrent()) patch({ history: { loading: false, data: entries } });
      } catch (error) {
        if (stillCurrent()) patch({ history: { loading: false, error: (error as Error).message } });
      }
      return;
    }

    if (section === 'structure') {
      patch({ structure: { loading: true } });
      try {
        const structure = (await GetTableStructure(
          ref.connId,
          ref.database,
          ref.schema,
          ref.table,
          tab.objectType === 'view' ? 'view' : 'table',
        )) as unknown as TableStructure;
        if (stillCurrent()) patch({ structure: { loading: false, data: structure } });
      } catch (error) {
        if (stillCurrent()) patch({ structure: { loading: false, error: (error as Error).message } });
      }
      return;
    }

    patch({ data: { loading: true } });
    const pageSize = tab.dataPageSize ?? DEFAULT_PAGE_SIZE;
    const page = Math.max(1, tab.dataPage ?? 1);
    const filters = tab.dataFilters ?? [];
    try {
      const [result, columns] = await Promise.all([
        PreviewTable(ref, pageSize, (page - 1) * pageSize, filters, tab.dataRequest ?? {}) as Promise<unknown> as Promise<QueryResult>,
        settings.showColumnComments
          ? GetColumns(ref.connId, ref.database, ref.schema, ref.table).catch(() => [])
          : Promise.resolve([]),
      ]);
      if (stillCurrent()) {
        patch({
          data: { loading: false, data: result },
          // The page that arrived is the one the next step is taken from, so its
          // own key edges replace the cursor that was asked for.
          dataRequest: undefined,
          dataEdges: { start: result.startCursor, end: result.endCursor },
          comments: commentMap(columns as unknown as ColumnInfo[]),
        });
      }
    } catch (error) {
      if (stillCurrent()) {
        // The rows that were on screen stay there: a card that empties itself
        // on a failed read also throws away the controls that could fix it.
        patch({ data: { loading: false, error: (error as Error).message, data: tab.data?.data } });
      }
    }
  };

  // A page is a fresh read of the same section, so it reuses the section's
  // sequence guard: an older page that arrives late is dropped.
  const showDataPage = (tab: Tab, page: number, pageSize: number) => {
    const target = Math.max(1, page);
    const shown = tab.dataPage ?? 1;
    // 上一页/下一页 is the only navigation the card has, so a single step always
    // has the page on screen to step off from. Anything else - another page
    // size, a jump, a refresh - is read by counting rows again.
    const request: PreviewCursor = {};
    if (pageSize === (tab.dataPageSize ?? DEFAULT_PAGE_SIZE)) {
      if (target === shown + 1) request.after = tab.dataEdges?.end;
      else if (target === shown - 1) request.before = tab.dataEdges?.start;
    }
    const next = { ...tab, dataPage: target, dataPageSize: pageSize, dataRequest: request };
    setTabs(current =>
      current.map(t =>
        t.id === tab.id
          ? { ...t, dataPage: target, dataPageSize: pageSize, dataRequest: request }
          : t
      )
    );
    loadObjectSection(next, 'data');
  };

  // A new condition changes which rows exist at all, so the page count it had
  // worked out no longer means anything: back to page one.
  const showDataFilters = (tab: Tab, filters: SqlFilter[]) => {
    setTabs(current =>
      current.map(t =>
        t.id === tab.id
          ? { ...t, dataFilters: filters, dataPage: 1, dataRequest: undefined, dataEdges: undefined }
          : t
      )
    );
    loadObjectSection(
      { ...tab, dataFilters: filters, dataPage: 1, dataRequest: undefined, dataEdges: undefined },
      'data'
    );
  };

  const openTableTab = (table: TableRef, label: string, objectType: 'table' | 'view') => {
    const existing = tabs.find(
      t => t.kind === 'table' && t.tableRef && objectKey(t.tableRef) === objectKey(table),
    );
    if (existing) {
      switchToTab(existing.id);
      return;
    }
    const tab: Tab = {
      id: `tab_${Date.now()}`,
      title: label,
      query: '',
      database: table.database,
      isDirty: false,
      connectionId: table.connId,
      kind: 'table',
      tableRef: table,
      objectType,
      section: 'data',
      data: { loading: true },
    };
    setTabs(current => [...current, tab]);
    setActiveTab(tab.id);
    loadObjectSection(tab, 'data');
  };

  // A key tab is a read of one Redis key. It has no editor and no write path:
  // the panel shows the value's rows and its type/TTL.
  const loadKVValue = async (tab: Tab) => {
    const ref = tab.tableRef;
    if (!ref) return;
    const key = `${tab.id}|kv`;
    const seq = (objectSeq.current.get(key) ?? 0) + 1;
    objectSeq.current.set(key, seq);
    const stillCurrent = () => (objectSeq.current.get(key) ?? 0) === seq;
    const patch = (next: Partial<Tab>) =>
      setTabs(current => current.map(t => (t.id === tab.id ? { ...t, ...next } : t)));

    patch({ kv: { loading: true, data: tab.kv?.data } });
    try {
      const value = (await GetKVValue(ref.connId, ref.database, ref.table)) as KVValue;
      if (stillCurrent()) patch({ kv: { loading: false, data: value } });
    } catch (error) {
      if (stillCurrent()) {
        patch({ kv: { loading: false, error: (error as Error).message, data: tab.kv?.data } });
      }
    }
  };

  const openKVTab = (ref: TableRef, label: string) => {
    const existing = tabs.find(
      t => t.kind === 'kv' && t.tableRef && objectKey(t.tableRef) === objectKey(ref),
    );
    if (existing) {
      switchToTab(existing.id);
      return;
    }
    const tab: Tab = {
      id: `tab_${Date.now()}`,
      title: label,
      query: '',
      database: ref.database,
      isDirty: false,
      connectionId: ref.connId,
      kind: 'kv',
      tableRef: ref,
      kv: { loading: true },
    };
    setTabs(current => [...current, tab]);
    setActiveTab(tab.id);
    loadKVValue(tab);
  };

  // An ER diagram is a read of every table in a schema; re-opening the same
  // database+schema focuses the existing tab rather than stacking duplicates.
  // 备份数据库：目标来自侧栏菜单，对话框只管收集模式/表/输出路径。
  // 结构可视编辑：目标与初始列来自对象面板的结构页。
  const [structureEditTarget, setStructureEditTarget] = useState<{
    ref: TableRef;
    columns: { name: string; type: string; nullable: boolean; defaultValue: string }[];
  } | null>(null);

  const [backupTarget, setBackupTarget] = useState<{
    connId: string;
    dbType: string;
    database: string;
    schema?: string;
  } | null>(null);

  // The schema whose "函数与过程" dialog is open (list + definition viewer).
  const [routinesTarget, setRoutinesTarget] = useState<{
    connId: string;
    database: string;
    schema?: string;
    title: string;
  } | null>(null);

  // The schema whose "枚举类型" dialog is open (PostgreSQL only).
  const [enumsTarget, setEnumsTarget] = useState<{
    connId: string;
    database: string;
    schema?: string;
    title: string;
  } | null>(null);

  const openERTab = (ref: TableRef) => {
    const existing = tabs.find(
      t => t.kind === 'er' && t.tableRef && objectKey(t.tableRef) === objectKey(ref),
    );
    if (existing) {
      switchToTab(existing.id);
      return;
    }
    const qualifier = ref.schema ? `${ref.database}.${ref.schema}` : ref.database;
    const tab: Tab = {
      id: `tab_${Date.now()}`,
      title: `ER ${qualifier}`,
      query: '',
      database: ref.database,
      isDirty: false,
      connectionId: ref.connId,
      kind: 'er',
      tableRef: ref,
    };
    setTabs(current => [...current, tab]);
    setActiveTab(tab.id);
  };

  // Schema comparison is a tool, not a view of one object: it starts from the
  // connection the navigator is on and lets the user pick both sides.
  const openCompareTab = () => {
    const existing = tabs.find(t => t.kind === 'compare');
    if (existing) {
      setActiveTab(existing.id);
      return;
    }
    const tab: Tab = {
      id: `tab_${Date.now()}`,
      title: '结构对比',
      query: '',
      isDirty: false,
      connectionId: activeConnection?.id,
      kind: 'compare',
      compareSource: { connId: activeConnection?.id ?? '', database: '', schema: '' },
    };
    setTabs(current => [...current, tab]);
    setActiveTab(tab.id);
  };

  // A query page starts from the table it belongs to, so the user can run it
  // as soon as the page opens. The quoted name comes from the backend.
  const defaultObjectQuery = async (tab: Tab) => {
    const ref = tab.tableRef;
    if (!ref) return '';
    try {
      return `select * from ${await SqlName(ref, 'local')}`;
    } catch {
      return '';
    }
  };

  const handleObjectSectionChange = (tab: Tab, section: ObjectSection) => {
    const needDefault = section === 'query' && !tab.objectQuery;
    setTabs(current => current.map(t => (t.id === tab.id ? { ...t, section } : t)));
    if (needDefault) {
      void defaultObjectQuery(tab).then(text => {
        if (!text) return;
        // Only fill a still-empty template: a late answer must not clobber
        // something the user typed in the meantime.
        setTabs(current =>
          current.map(t => (t.id === tab.id && !t.objectQuery ? { ...t, objectQuery: text } : t))
        );
      });
    }
    if (section === 'query') return;
    const state =
      section === 'structure' ? tab.structure : section === 'history' ? tab.history : tab.data;
    if (!state?.data && !state?.loading) loadObjectSection(tab, section);
  };

  const handleObjectRefresh = (tab: Tab) => {
    const section = tab.section ?? 'data';
    if (section !== 'query') loadObjectSection(tab, section);
  };

  const handleObjectExecute = async (tab: Tab) => {
    const ref = tab.tableRef;
    const sql = tab.objectQuery?.trim();
    if (!ref || !sql) return;
    const database = tabDatabase(tab);
    const key = `${tab.id}|query`;
    const seq = (objectSeq.current.get(key) ?? 0) + 1;
    objectSeq.current.set(key, seq);
    const stillCurrent = () => (objectSeq.current.get(key) ?? 0) === seq;
    const patch = (next: Partial<Tab>) =>
      setTabs(current => current.map(t => (t.id === tab.id ? { ...t, ...next } : t)));

    patch({ objectResult: { loading: true }, objectComments: undefined });
    try {
      const result = await runGuarded(ref.connId, sql, database, undefined, tab.id);
      const comments =
        settings.showColumnComments && !result.error
          ? await queryComments(ref.connId, sql, database)
          : undefined;
      const source = result.error ? null : await GetQuerySource(ref.connId, sql, database);
      if (stillCurrent()) {
        patch({
          objectResult: { loading: false, data: result },
          objectComments: comments,
          objectSource: source,
        });
      }
    } catch (error) {
      if (stillCurrent()) {
        patch({ objectResult: { loading: false, error: (error as Error).message }, objectSource: null });
      }
    }
  };

  // Clicking a history entry moves the statement to the query page without
  // running it, so the user still sees what is about to happen.
  const handleUseStatement = (tab: Tab, sql: string) => {
    setTabs(current =>
      current.map(t => (t.id === tab.id ? { ...t, section: 'query', objectQuery: sql } : t))
    );
  };

  const handleClearHistory = async (tab: Tab) => {
    const ref = tab.tableRef;
    if (!ref) return;
    try {
      await ClearHistory({ connId: ref.connId, database: ref.database, table: ref.table });
      loadObjectSection(tab, 'history');
    } catch (error) {
      console.error('Failed to clear history:', error);
    }
  };

  const clearObjectSection = (tabId: string, section: ObjectSection) => {
    setTabs(current =>
      current.map(tab => {
        if (tab.id !== tabId) return tab;
        switch (section) {
          case 'structure':
            return { ...tab, structure: undefined };
          case 'history':
            return { ...tab, history: undefined };
          case 'query':
            return { ...tab, objectResult: undefined, objectComments: undefined, objectSource: null };
          default:
            // Dismissing the banner clears the message, not the page: the rows
            // are what the filter bar and the pager need to keep working.
            return { ...tab, data: { loading: false, data: tab.data?.data }, comments: undefined };
        }
      })
    );
  };

  const startResize = (e: React.PointerEvent) => {
    e.preventDefault();
    const startY = e.clientY;
    const startHeight = editorHeight;
    const move = (ev: PointerEvent) => {
      setEditorHeight(clampEditorHeight(startHeight + ev.clientY - startY));
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const nudgeEditorHeight = (delta: number) =>
    setEditorHeightState(current => {
      const next = clampEditorHeight(current + delta);
      try {
        localStorage.setItem('tablepro.editorHeight', String(next));
      } catch { /* private mode: the choice just does not survive */ }
      return next;
    });


  const handleInsertText = async (table: TableRef) => {
    const objectTab = tabs.find(
      t => t.id === activeTab && t.kind === 'table' && t.section === 'query',
    );
    // An object tab has no editor to write into, so double-clicking starts a
    // statement of its own instead.
    const editorTab = objectTab
      ? undefined
      : tabs.find(t => t.id === activeTab && t.kind !== 'table' && t.kind !== 'kv');
    const targetDatabase = objectTab
      ? tabDatabase(objectTab)
      : editorTab?.database || activeConnection?.database || '';
    let text: string;
    try {
      text = await SqlName(table, table.database === targetDatabase ? 'local' : 'qualified');
    } catch {
      // The backend refused to quote this name; inserting it unquoted is
      // exactly what this path no longer does.
      return;
    }
    if (objectTab) {
      if (!objectTab.objectQuery?.trim()) {
        setTabs(current =>
          current.map(t => (t.id === objectTab.id ? { ...t, objectQuery: `select * from ${text}` } : t))
        );
      } else {
        objectEditorRef.current?.insertAtCursor(text);
      }
      return;
    }

    if (!editorTab) {
      const newTab: Tab = {
        id: `tab_${Date.now()}`,
        title: `Query ${tabs.length + 1}`,
        query: `SELECT * FROM ${text}`,
        database: tabDatabase(),
        isDirty: true,
        connectionId: activeConnection?.id,
      };
      setTabs([...tabs, newTab]);
      setActiveTab(newTab.id);
      return;
    }
    editorRef.current?.insertAtCursor(text);
  };

  // A DDL statement makes every name-level cache for that connection wrong, so
  // it goes with the connection rather than being patched one entry at a time.
  const invalidateCatalog = (connId: string) => {
    for (const cache of [tableCache, columnCache, commentCache]) {
      for (const key of [...cache.current.keys()]) {
        if (key.split('|')[0] === connId) cache.current.delete(key);
      }
    }
  };

  const objectTabFor = (ref: TableRef) =>
    tabs.find(t => t.kind === 'table' && t.tableRef && objectKey(t.tableRef) === objectKey(ref));

  const handleRenameObject = async (ref: TableRef, newName: string) => {
    await RunTableAction(ref, 'rename', newName);
    invalidateCatalog(ref.connId);
    const open = objectTabFor(ref);
    if (!open) return;
    const next: Tab = {
      ...open,
      title: newName,
      tableRef: { ...open.tableRef as TableRef, table: newName },
      data: { loading: true },
      structure: undefined,
      history: undefined,
      comments: undefined,
    };
    setTabs(current => current.map(t => (t.id === open.id ? next : t)));
    loadObjectSection(next, next.section ?? 'data');
  };

  // The object is gone, so the tab has nothing left to show; the tree loses the
  // node when the sidebar refetches the branch.
  const handleDropObject = async (ref: TableRef) => {
    await RunTableAction(ref, 'drop');
    invalidateCatalog(ref.connId);
    const open = objectTabFor(ref);
    if (!open) return;
    const kept = tabs.filter(t => t.id !== open.id);
    setTabs(kept);
    if (activeTab === open.id) setActiveTab(kept.length > 0 ? kept[0].id : null);
  };

  // Rows were pulled out from under the grid; its queue is keyed to them, so it
  // is reloaded rather than left showing data that no longer exists.
  const handleTruncateObject = async (ref: TableRef) => {
    await RunTableAction(ref, 'truncate');
    const open = objectTabFor(ref);
    if (open && (open.section ?? 'data') === 'data') loadObjectSection(open, 'data');
  };

  const kvTabFor = (ref: TableRef) =>
    tabs.find(t => t.kind === 'kv' && t.tableRef && objectKey(t.tableRef) === objectKey(ref));

  // Rename keeps the open value tab and re-points it at the new key, so the
  // user is not left with a tab that will 404 on its next refresh.
  const handleRenameKVKey = async (ref: TableRef, newName: string) => {
    await RenameKVKey(ref.connId, ref.database, ref.table, newName);
    const open = kvTabFor(ref);
    if (!open) return;
    const next: Tab = {
      ...open,
      title: newName,
      tableRef: { ...(open.tableRef as TableRef), table: newName },
      kv: { loading: true, data: open.kv?.data },
    };
    setTabs(current => current.map(t => (t.id === open.id ? next : t)));
    loadKVValue(next);
  };

  const handleDeleteKVKey = async (ref: TableRef) => {
    await DeleteKVKey(ref.connId, ref.database, ref.table);
    const open = kvTabFor(ref);
    if (open) closeTabForReal(open.id);
  };

  const handleSetKVKeyTTL = async (ref: TableRef, seconds: number) => {
    await SetKVKeyTTL(ref.connId, ref.database, ref.table, seconds);
    const open = kvTabFor(ref);
    if (open) loadKVValue(open);
  };

  // 新建表 is a statement to edit and run, not one the app fires off: the name
  // and columns are the user's to decide.
  const handleNewTable = async (target: NewTableTarget) => {
    const tab: Tab = {
      id: `tab_${Date.now()}`,
      title: '新建表',
      query: await GetCreateTableTemplate(target.dbType, target.schema),
      database: target.database,
      isDirty: true,
      connectionId: target.connId,
    };
    setTabs([...tabs, tab]);
    setActiveTab(tab.id);
  };

  // Switching the target database only affects the next run, and the comments
  // already on screen belong to the previous one.
  const handleTabDatabaseChange = (tabId: string, database: string) => {
    setTabs(current =>
      current.map(tab => (tab.id === tabId ? { ...tab, database, comments: undefined, source: null } : tab))
    );
  };

  const handleAddConnection = async (config: ConnectionConfig) => {
    try {
      await AddConnection(config);
      await loadConnections();
      setShowConnectionDialog(false);
      toast.success(`已添加连接 ${config.name}`);
    } catch (error) {
      console.error('Failed to add connection:', error);
      toast.error(`保存连接失败：${(error as Error).message}`, { duration: 8000 });
    }
  };

  const handleUpdateConnection = async (config: ConnectionConfig) => {
    try {
      await UpdateConnection(config);
      await loadConnections();
      setShowConnectionDialog(false);
      setEditingConnection(null);
      toast.success(`已保存 ${config.name}`);
    } catch (error) {
      console.error('Failed to update connection:', error);
      toast.error(`保存连接失败：${(error as Error).message}`, { duration: 8000 });
    }
  };

  // The backend re-reads the filter from the saved config, so an open
  // connection picks it up on its next refresh without reconnecting.
  const handleSaveDatabaseFilter = async (connection: Connection, filter: DatabaseFilter) => {
    await UpdateConnection({ ...connection, password: connection.password ?? '', databaseFilter: filter });
    await loadConnections();
    // The tab's database picker keeps its own copy of the server list, which
    // would otherwise still offer the databases the navigator just hid.
    loadedDatabases.current.delete(connection.id);
    if (activeConnection?.id === connection.id) {
      const dbs = await GetDatabases(connection.id).catch(() => null);
      if (dbs) {
        loadedDatabases.current.set(connection.id, dbs);
        setDatabases(dbs);
      }
    }
  };

  const handleDeleteConnection = async (id: string) => {
    try {
      await DeleteConnection(id);
      if (activeConnection?.id === id) {
        await handleDisconnect(id);
      }
      await loadConnections();
    } catch (error) {
      console.error('Failed to delete connection:', error);
    }
  };

  const handleTestConnection = async (config: ConnectionConfig): Promise<boolean> => {
    try {
      await TestConnection(config);
      return true;
    } catch (error) {
      throw error;
    }
  };

  const handleEditConnection = (connection: Connection) => {
    setEditingConnection({ ...connection, password: connection.password || '' });
    setShowConnectionDialog(true);
  };

  const activeTabData = tabs.find(t => t.id === activeTab);
  // An error result is just a banner, so let the editor absorb that space
  // instead of leaving a blank result pane under it.
  const isErrorResult = !!activeTabData?.result?.error && !activeTabData?.loading;
  const objectRef = activeTabData?.kind === 'table' ? activeTabData.tableRef : undefined;

  const clearResult = (tabId: string) => {
    setTabs(current =>
      current.map(tab => (tab.id === tabId ? { ...tab, result: undefined, comments: undefined, source: null } : tab))
    );
  };

  const openFromQuickSearch = (match: ObjectMatch) => {
    setQuickOpen(false);
    openTableTab(
      { connId: match.connId, database: match.database, schema: match.schema, table: match.table },
      match.table,
      match.type === 'view' ? 'view' : 'table',
    );
  };

  // App-wide keys, bound here rather than in a widget so they work no matter
  // which panel has the focus.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'F1') {
        event.preventDefault();
        setShowShortcuts(current => !current);
        return;
      }
      if (!(event.ctrlKey || event.metaKey)) return;
      const key = event.key.toLowerCase();
      if (key === 'p') {
        event.preventDefault();
        setQuickOpen(current => !current);
      } else if (key === '/') {
        event.preventDefault();
        setShowShortcuts(current => !current);
      } else if (key === 't') {
        event.preventDefault();
        handleNewTab();
      } else if (key === 'w') {
        if (activeTab) {
          event.preventDefault();
          handleCloseTab(activeTab);
        }
      } else if (key >= '1' && key <= '9') {
        const tab = tabs[Number(key) - 1];
        if (tab) {
          event.preventDefault();
          setActiveTab(tab.id);
        }
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabs, activeTab]);

  return (
    <div className="flex h-screen bg-canvas text-ink">
      <Sidebar
        connections={connections}
        activeConnection={activeConnection}
        connectedIds={connectedIds}
        onConnect={handleConnect}
        onDisconnect={handleDisconnect}
        onNewConnection={() => {
          setEditingConnection(null);
          setShowConnectionDialog(true);
        }}
        onEditConnection={handleEditConnection}
        onDeleteConnection={handleDeleteConnection}
        onSaveDatabaseFilter={handleSaveDatabaseFilter}
        onOpenTable={openTableTab}
        onOpenKey={openKVTab}
        onInsertText={handleInsertText}
        onNewTable={handleNewTable}
        onOpenER={openERTab}
        onBackup={node =>
          setBackupTarget({
            connId: node.connId,
            dbType: node.dbType,
            database: (node.database as string) ?? '',
            schema: node.schema ?? '',
          })
        }
        onRoutines={node =>
          setRoutinesTarget({
            connId: node.connId,
            database: (node.database as string) ?? '',
            schema: node.schema ?? '',
            title: [node.database, node.schema].filter(Boolean).join('.') || node.label,
          })
        }
        onEnums={node =>
          setEnumsTarget({
            connId: node.connId,
            database: (node.database as string) ?? '',
            schema: node.schema ?? '',
            title: [node.database, node.schema].filter(Boolean).join('.') || node.label,
          })
        }
        onRenameObject={handleRenameObject}
        onDropObject={handleDropObject}
        onTruncateObject={handleTruncateObject}
        onRenameKey={handleRenameKVKey}
        onDeleteKey={handleDeleteKVKey}
        onSetKeyTTL={handleSetKVKeyTTL}
        onConfirm={setConfirmReq}
      />

      <div className="flex-1 flex flex-col min-w-0">
        <div className="flex bg-panel border-b border-line items-center">
          <div className="flex flex-1 min-w-0 overflow-x-auto">
          {tabs.map(tab => {
            const ref = tab.kind === 'table' ? tab.tableRef : undefined;
            const kvRef = tab.kind === 'kv' ? tab.tableRef : undefined;
            const erRef = tab.kind === 'er' ? tab.tableRef : undefined;
            const fullName = ref
              ? `${objectQualifier(ref, connections.find(c => c.id === ref.connId)?.type)}.${ref.table}`
              : kvRef
                ? `db ${kvRef.database} · ${kvRef.table}`
                : undefined;
            return (
              <div
                key={tab.id}
                className={`flex items-center shrink-0 px-4 py-2 cursor-pointer border-r border-line ${
                  activeTab === tab.id ? 'bg-raised' : 'hover:bg-raised/60'
                }`}
                onClick={() => switchToTab(tab.id)}
                title={fullName}
              >
                {ref &&
                  (tab.objectType === 'view'
                    ? <FiEye size={12} aria-hidden className="mr-1.5 shrink-0 text-icon-view" />
                    : <FiTable size={12} aria-hidden className="mr-1.5 shrink-0 text-success" />)}
                {kvRef && <FiKey size={12} aria-hidden className="mr-1.5 shrink-0 text-accent" />}
                {erRef && <FiGitBranch size={12} aria-hidden className="mr-1.5 shrink-0 text-accent" />}
                <span className="text-sm max-w-[200px] truncate">{tab.title}</span>
                {tab.isDirty && <span className="ml-2 text-warning">*</span>}
                <button
                  className="ml-1 flex items-center justify-center w-5 h-5 text-secondary hover:text-ink"
                  aria-label={`关闭 ${tab.title} 标签页`}
                  onClick={(e) => {
                    e.stopPropagation();
                    handleCloseTab(tab.id);
                  }}
                >
                  ×
                </button>
              </div>
            );
          })}
          </div>
          <button
            className="w-8 shrink-0 text-secondary hover:text-ink hover:bg-raised/60"
            onClick={handleNewTab}
            aria-label="新建查询标签页"
            title="新建查询标签页"
          >
            +
          </button>
          <div className="ml-auto flex items-center gap-1 pr-2">
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={openCompareTab}
              title="结构对比 / 同步：把一个库的结构同步到另一个"
              aria-label="结构对比与同步"
            >
              <FiShuffle size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowAiPanel(current => !current)}
              title="AI 助手：问数据库"
              aria-label="AI 助手"
            >
              <FiMessageCircle size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setQuickOpen(true)}
              title="快速打开表 / 视图 (Ctrl+P)"
              aria-label="快速打开表或视图"
            >
              <FiSearch size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowShortcuts(true)}
              title="快捷键 (F1)"
              aria-label="查看快捷键说明"
            >
              <FiHelpCircle size={14} />
            </button>
            <ThemePicker
              theme={settings.theme}
              onChange={theme => changeSettings({ ...settings, theme })}
            />
          </div>
        </div>

        <div className="flex-1 flex flex-col min-h-0">
          {activeTabData?.kind === 'compare' ? (
            <CompareView
              connections={connections}
              connectedIds={connectedIds}
              initialSource={activeTabData.compareSource}
            />
          ) : activeTabData?.kind === 'er' ? (
            <ERDiagram
              connId={activeTabData.tableRef?.connId ?? ''}
              database={activeTabData.tableRef?.database ?? ''}
              schema={activeTabData.tableRef?.schema ?? ''}
              title={activeTabData.title}
              theme={settings.theme}
            />
          ) : activeTabData && objectRef ? (
            <ObjectPanel
              tableName={objectRef.table}
              qualifier={objectQualifier(
                objectRef,
                connections.find(c => c.id === objectRef.connId)?.type,
              )}
              objectType={activeTabData.objectType ?? 'table'}
              section={activeTabData.section ?? 'data'}
              onSectionChange={section => handleObjectSectionChange(activeTabData, section)}
              data={activeTabData.data ?? { loading: false }}
              dataPage={activeTabData.dataPage ?? 1}
              dataPageSize={activeTabData.dataPageSize ?? DEFAULT_PAGE_SIZE}
              onDataPage={page =>
                showDataPage(activeTabData, page, activeTabData.dataPageSize ?? DEFAULT_PAGE_SIZE)
              }
              onDataPageSize={size => showDataPage(activeTabData, 1, size)}
              dataFilters={activeTabData.dataFilters ?? []}
              onDataFilters={filters => showDataFilters(activeTabData, filters)}
              structure={activeTabData.structure ?? { loading: false }}
              history={activeTabData.history ?? { loading: false }}
              comments={activeTabData.comments}
              showComments={settings.showColumnComments}
              editSource={activeTabData.objectType === 'view' ? null : objectRef}
              exportName={`${objectRef.database}.${objectRef.table}`}
              tableRef={activeTabData.tableRef ?? null}
              onEditStructure={() => {
                if (!activeTabData.tableRef || !activeTabData.structure?.data) return;
                setStructureEditTarget({
                  ref: activeTabData.tableRef,
                  columns: activeTabData.structure.data.columns.map(c => ({
                    name: c.name,
                    type: c.type,
                    nullable: c.nullable,
                    defaultValue: c.defaultValue ?? '',

                  })),
                });
              }}
              onImport={() => setImportTarget(objectRef)}
              onRefresh={() => handleObjectRefresh(activeTabData)}
              onDataApplied={() => loadObjectSection(activeTabData, 'data')}
              onPendingChange={total => pendingEdits.current.set(activeTabData.id, total)}
              onDismissError={section => clearObjectSection(activeTabData.id, section)}
              onUseStatement={sql => handleUseStatement(activeTabData, sql)}
              onClearHistory={() => handleClearHistory(activeTabData)}
              queryPage={
                <ObjectQueryPage
                  sql={activeTabData.objectQuery ?? ''}
                  onSqlChange={sql =>
                    setTabs(current =>
                      current.map(t => (t.id === activeTabData.id ? { ...t, objectQuery: sql } : t))
                    )
                  }
                  onExecute={() => handleObjectExecute(activeTabData)}
                  onCancel={() => {
                    if (!activeConnection) return;
                    CancelQuery(activeConnection.id).catch(() => {});
                    cancelObjectQuery(activeTabData.id);
                  }}
                  loading={!!activeTabData.objectResult?.loading}
                  isConnected={!!activeConnection}
                  dialect={activeConnection?.type ?? 'mysql'}
                  databases={databases}
                  database={tabDatabase(activeTabData)}
                  onDatabaseChange={db => handleTabDatabaseChange(activeTabData.id, db)}
                  settings={settings}
                  onSettingsChange={changeSettings}
                  getTables={completionTables}
                  getColumns={completionColumns}
                  result={activeTabData.objectResult ?? { loading: false }}
                  comments={activeTabData.objectComments}
                  editSource={activeTabData.objectSource}
                  onApplied={() => handleObjectExecute(activeTabData)}
                  onPendingChange={total => pendingEdits.current.set(activeTabData.id, total)}
                  exportName={`${objectRef.database}.${objectRef.table}`}
                  onDismissError={() => clearObjectSection(activeTabData.id, 'query')}
                  editorRef={objectEditorRef}
                  height={editorHeight}
                  onResizeStart={startResize}
                  onResizeStep={nudgeEditorHeight}
                />
              }
            />
          ) : activeTabData?.kind === 'kv' ? (
            <KvValuePanel
              connId={activeTabData.tableRef?.connId ?? ''}
              database={activeTabData.database ?? ''}
              kv={activeTabData.kv ?? { loading: false }}
              onRefresh={() => loadKVValue(activeTabData)}
              onDismissError={() =>
                setTabs(current =>
                  current.map(t =>
                    t.id === activeTabData.id
                      ? { ...t, kv: { loading: false, data: t.kv?.data } }
                      : t,
                  ),
                )
              }
            />
          ) : activeTabData ? (
            <>
              <div
                style={isErrorResult ? undefined : { height: editorHeight }}
                className={`min-h-0 ${isErrorResult ? 'flex-1' : 'shrink-0'}`}
              >
                <QueryEditor
                  ref={editorRef}
                  query={activeTabData.query}
                  onQueryChange={(query) => {
                    setTabs(tabs.map(tab =>
                      tab.id === activeTab
                        ? { ...tab, query, isDirty: true }
                        : tab
                    ));
                  }}
                  onExecute={() => handleExecuteQuery(activeTabData)}
                  onCancel={() => {
                    if (!activeConnection) return;
                    // Stop the server-side statement, then clear the spinner:
                    // bumping the tab's sequence makes the trailing stream
                    // events no-ops, so the rows already painted stay visible.
                    CancelQuery(activeConnection.id).catch(() => {});
                    cancelQuery(activeTabData.id);
                  }}
                  isConnected={!!activeConnection}
                  loading={!!activeTabData.loading}
                  dialect={activeConnection?.type ?? 'mysql'}
                  databases={databases}
                  database={tabDatabase(activeTabData)}
                  onDatabaseChange={db => handleTabDatabaseChange(activeTabData.id, db)}
                  settings={settings}
                  onSettingsChange={changeSettings}
                  getTables={completionTables}
                  getColumns={completionColumns}
                  {...txHandlers(activeTabData)}
                />
              </div>

              <RowSplitter
                onPointerDown={startResize}
                onStep={nudgeEditorHeight}
                label="编辑器高度"
                valueNow={editorHeight}
                valueMin={EDITOR_MIN_HEIGHT}
                valueMax={editorMaxHeight()}
              />

              {activeTabData.loading && (
                <div className="flex-1 flex flex-col items-center justify-center gap-2 text-muted text-sm border-t border-line">
                  <FiLoader className="animate-spin" size={18} />
                  <span>执行中…</span>
                  <button
                    className="px-2 py-0.5 rounded text-2xs text-secondary border border-line hover:text-ink hover:bg-raised"
                    onClick={() => cancelQuery(activeTabData.id)}
                    title="结果不再更新，仅隐藏等待提示（服务端仍会继续执行）"
                  >
                    取消等待
                  </button>
                </div>
              )}

              {!activeTabData.loading && activeTabData.result && (
                <div className={isErrorResult ? 'shrink-0' : 'flex-1 flex flex-col min-h-0'}>
                  <DataGrid
                    result={activeTabData.result}
                    comments={activeTabData.comments}
                    showComments={settings.showColumnComments}
                    editSource={activeTabData.source}
                    onApplied={() => handleExecuteQuery(activeTabData)}
                    onPendingChange={total => pendingEdits.current.set(activeTabData.id, total)}
                    onDismissError={() => clearResult(activeTabData.id)}
                    exportName={`${tabDatabase(activeTabData)}-${activeTabData.title}`}
                  />
                </div>
              )}
            </>
          ) : (
            <div className="flex-1 flex items-center justify-center text-muted">
              <div className="text-center">
                <p className="text-2xl mb-2">TablePro Go</p>
                <p className="text-sm">Create a new query tab to get started</p>
              </div>
            </div>
          )}
        </div>
      </div>

      {showConnectionDialog && (
        <ConnectionDialog
          initialConfig={editingConnection}
          onAdd={handleAddConnection}
          onUpdate={handleUpdateConnection}
          onClose={() => {
            setShowConnectionDialog(false);
            setEditingConnection(null);
          }}
          onTest={handleTestConnection}
        />
      )}

      {backupTarget && (
        <BackupDialog target={backupTarget} onClose={() => setBackupTarget(null)} />
      )}

      {routinesTarget && (
        <RoutinesDialog target={routinesTarget} onClose={() => setRoutinesTarget(null)} />
      )}

      {enumsTarget && (
        <EnumsDialog target={enumsTarget} onClose={() => setEnumsTarget(null)} />
      )}

      {structureEditTarget && (
        <StructureEditorDialog
          target={structureEditTarget.ref}
          columns={structureEditTarget.columns}
          onApplied={() => {
            const tab = tabs.find(t => t.tableRef && objectKey(t.tableRef) === objectKey(structureEditTarget.ref));
            if (tab) loadObjectSection(tab, 'structure');
          }}
          onClose={() => setStructureEditTarget(null)}
        />
      )}

      {quickOpen && (
        <QuickOpen
          connections={connections}
          search={SearchObjects}
          onPick={openFromQuickSearch}
          onClose={() => setQuickOpen(false)}
        />
      )}

      {showShortcuts && <ShortcutHelp onClose={() => setShowShortcuts(false)} />}

      {confirmReq && (
        <ConfirmDialog request={confirmReq} onDismiss={() => setConfirmReq(null)} />
      )}

      {importTarget && (
        <ImportDialog
          target={importTarget}
          onClose={() => setImportTarget(null)}
          onImported={() => {
            if (activeTabData) handleObjectRefresh(activeTabData);
          }}
        />
      )}

      <Toaster position="bottom-right" />
    </div>
  );
}

export default App;
