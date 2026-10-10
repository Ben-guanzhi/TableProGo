import { useState, useEffect, useRef } from 'react';
import { FiEye, FiHelpCircle, FiLoader, FiSearch, FiTable } from 'react-icons/fi';
import { Toaster } from 'react-hot-toast';
import Sidebar, { NewTableTarget } from './components/Sidebar';
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
import ShortcutHelp from './components/ShortcutHelp';
import ThemePicker from './components/ThemePicker';
import ConnectionDialog from './components/ConnectionDialog';
import ConfirmDialog, { ConfirmRequest } from './components/ConfirmDialog';
import {
  Connection,
  DatabaseFilter,
  Tab,
  QueryResult,
  SqlFilter,
  ColumnInfo,
  TableStructure,
  HistoryEntry,
  ObjectSection,
  PreviewCursor,
} from './types';
import {
  GetConnections,
  Connect,
  Disconnect,
  ExecuteQuery,
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
} from './api';
import { applyTheme } from './theme';
// The starter DDL comes from the shared SQL text package through the
// generated binding, so the webview and the Fyne client open on one template.
import { GetCreateTableTemplate } from '@bindings/tablepro-go/app';

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

const defaultSettings: AppSettings = {
  showColumnComments: false,
  resultLimit: 1000,
  theme: 'system',
};

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
  const [editingConnection, setEditingConnection] = useState<Connection | null>(null);
  const [settings, setSettings] = useState<AppSettings>(defaultSettings);
  const [editorHeight, setEditorHeight] = useState(200);
  const [databases, setDatabases] = useState<string[]>([]);
  const [quickOpen, setQuickOpen] = useState(false);
  const [showShortcuts, setShowShortcuts] = useState(false);
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
    GetSettings()
      .then(s => setSettings(s ?? defaultSettings))
      .catch(() => {});
  }, []);

  // The palette lives in CSS variables, so painting the theme is just setting
  // one attribute; "system" tracks the OS preference live.
  useEffect(() => {
    applyTheme(settings.theme);
    if (settings.theme !== 'system') return;
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = () => applyTheme('system');
    media.addEventListener('change', onChange);
    return () => media.removeEventListener('change', onChange);
  }, [settings.theme]);

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
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [activeConnection]);

  const tabDatabase = (tab?: Tab) => tab?.database || activeConnection?.database || '';

  const changeSettings = (next: AppSettings) => {
    setSettings(next);
    SaveSettings(next).catch(error => console.error('Failed to save settings:', error));
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
    }
  };

  const handleConnect = async (connection: Connection) => {
    await Connect(connection.id);
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
      // rather than leaving a dead grid on screen.
      const kept = tabs.filter(t => !(t.kind === 'table' && t.tableRef?.connId === connId));
      if (kept.length !== tabs.length) {
        setTabs(kept);
        if (activeTab && !kept.some(t => t.id === activeTab)) {
          setActiveTab(kept.length > 0 ? kept[0].id : null);
        }
      }
    } catch (error) {
      console.error('Failed to disconnect:', error);
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

  const cancelQuery = (tabId: string) => {
    runSeq.current.set(tabId, (runSeq.current.get(tabId) ?? 0) + 1);
    setTabs(current => current.map(t => (t.id === tabId ? { ...t, loading: false } : t)));
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
      const result = await ExecuteQuery(activeConnection.id, tab.query, database) as unknown as QueryResult;
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
      setActiveTab(existing.id);
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
      const result = (await ExecuteQuery(ref.connId, sql, database)) as unknown as QueryResult;
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
    setEditorHeight(current => clampEditorHeight(current + delta));


  const handleInsertText = async (table: TableRef) => {
    const objectTab = tabs.find(
      t => t.id === activeTab && t.kind === 'table' && t.section === 'query',
    );
    // An object tab has no editor to write into, so double-clicking starts a
    // statement of its own instead.
    const editorTab = objectTab
      ? undefined
      : tabs.find(t => t.id === activeTab && t.kind !== 'table');
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

  const handleAddConnection = async (config: any) => {
    try {
      await AddConnection(config);
      await loadConnections();
      setShowConnectionDialog(false);
    } catch (error) {
      console.error('Failed to add connection:', error);
    }
  };

  const handleUpdateConnection = async (config: any) => {
    try {
      await UpdateConnection(config);
      await loadConnections();
      setShowConnectionDialog(false);
      setEditingConnection(null);
    } catch (error) {
      console.error('Failed to update connection:', error);
    }
  };

  // The backend re-reads the filter from the saved config, so an open
  // connection picks it up on its next refresh without reconnecting.
  const handleSaveDatabaseFilter = async (connection: Connection, filter: DatabaseFilter) => {
    await UpdateConnection({ ...connection, databaseFilter: filter });
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

  const handleTestConnection = async (config: any): Promise<boolean> => {
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
        onInsertText={handleInsertText}
        onNewTable={handleNewTable}
        onRenameObject={handleRenameObject}
        onDropObject={handleDropObject}
        onTruncateObject={handleTruncateObject}
        onConfirm={setConfirmReq}
      />

      <div className="flex-1 flex flex-col min-w-0">
        <div className="flex bg-panel border-b border-line items-center">
          <div className="flex flex-1 min-w-0 overflow-x-auto">
          {tabs.map(tab => {
            const ref = tab.kind === 'table' ? tab.tableRef : undefined;
            const fullName = ref
              ? `${objectQualifier(ref, connections.find(c => c.id === ref.connId)?.type)}.${ref.table}`
              : undefined;
            return (
              <div
                key={tab.id}
                className={`flex items-center shrink-0 px-4 py-2 cursor-pointer border-r border-line ${
                  activeTab === tab.id ? 'bg-raised' : 'hover:bg-raised/60'
                }`}
                onClick={() => setActiveTab(tab.id)}
                title={fullName}
              >
                {ref &&
                  (tab.objectType === 'view'
                    ? <FiEye size={12} aria-hidden className="mr-1.5 shrink-0 text-icon-view" />
                    : <FiTable size={12} aria-hidden className="mr-1.5 shrink-0 text-success" />)}
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
          {activeTabData && objectRef ? (
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

      <Toaster position="bottom-right" />
    </div>
  );
}

export default App;
