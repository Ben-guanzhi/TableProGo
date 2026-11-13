import { useState, useEffect, useRef, useCallback } from 'react';
import { FiActivity, FiBookOpen, FiBox, FiCode, FiCpu, FiDownload, FiFileText, FiInfo, FiEye, FiGitBranch, FiHelpCircle, FiKey, FiLoader, FiMessageCircle, FiMessageSquare, FiMonitor, FiPackage, FiRefreshCw, FiSearch, FiShare2, FiShield, FiShuffle, FiTable, FiTerminal } from 'react-icons/fi';
import { Toaster } from 'react-hot-toast';
import Sidebar, { NewTableTarget } from './components/Sidebar';
import BackupDialog from './components/BackupDialog';
import RoutinesDialog from './components/RoutinesDialog';
import EnumsDialog from './components/EnumsDialog';
import DataSearchDialog from './components/DataSearchDialog';
import LockScreen from './components/LockScreen';
import PasscodeDialog from './components/PasscodeDialog';
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
import TransferDialog from './components/TransferDialog';
import RowSyncDialog from './components/RowSyncDialog';
import PluginPanel from './components/PluginPanel';
import ExplainPlanView from './components/ExplainPlanView';
import ImportConnectionsDialog from './components/ImportConnectionsDialog';
import ObjectHealthDialog from './components/ObjectHealthDialog';
import AboutDialog from './components/AboutDialog';
import NotebookPanel from './components/NotebookPanel';
import LoginScreen from './components/LoginScreen';
import PluginConsole from './components/PluginConsole';
import TerminalPanel from './components/TerminalPanel';
import FilesPanel from './components/FilesPanel';
import MonitorPanel from './components/MonitorPanel';
import ContainersPanel from './components/ContainersPanel';
import MqttPanel from './components/MqttPanel';
import NotesPanel from './components/NotesPanel';
import DesktopPanel from './components/DesktopPanel';
import AiProviderPanel from './components/AiProviderPanel';
import ExtensionsPanel from './components/ExtensionsPanel';
import KvValuePanel from './components/KvValuePanel';
import ERDiagram from './components/ERDiagram';
import { foreignKeyEditRows, indexEditRows } from './lib/structureEdit';
import type { ForeignKeyEditRow, IndexEditRow } from './lib/structureEdit';
import { txBusy, txGate, txSettle, type TxOp } from './lib/txButtons';
import {
  Connection,
  ConnectionConfig,
  DatabaseFilter,
  EngineType,
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
  DisconnectAll,
  CancelQuery,
  SecurityNotice,
  GetDatabases,
  AddConnection,
  UpdateConnection,
  DeleteConnection,
  TestConnection,
  PreviewTable,
  RunTableAction,
  ExplainQuery,
  ExplainPlanTree,
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
  GetKVValuePage,
  RenameKVKey,
  DeleteKVKey,
  SetKVKeyTTL,
  GetSchemas,
  GetRoutines,
  ExportWorkbook,
} from './api';
import { mergeKVWindow } from './kvPaging';
import { applyTheme } from './theme';
import toast from 'react-hot-toast';
import { runGuarded, runStreamed } from './lib/queryRun';
import { isMagicCommand, parseMagicCommand, runMagicCommand, MAGIC_HELP } from './lib/magicCommands';
import { activePaneOf, paneSQL, toggleDualPane } from './lib/dualPane';
import { duplicateCombos, findShortcut, normalizeCombo } from './lib/shortcuts';
import { fileStem, privateTitle } from './lib/privacy';
import { AuthRequiredError, SAME_ORIGIN } from './api';
import { download, downloadBlob } from './datagrid/download';
import { toCSV, toJSON, toMarkdown, toTSV } from './gridView';
import {
  BeginTransaction,
  CommitTransaction,
  ExecuteQueryInTab,
  ForgetTxSession,
  RollbackTransaction,
  TransactionStatusDetail,
} from './api';
import { defaultSettings, saveSettings, useLoadSettings, useSettingsEffect } from './lib/useSettings';
import { useIdleLock } from './lib/useIdleLock';

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

// New tab ids: Date.now() alone collides when two tabs appear in the same
// millisecond (a double click on "new tab"), which would corrupt the React keys
// and make switchToTab land on the wrong tab. The counter keeps ids unique
// within the process.
let tabIdSeq = 0;
const newTabId = () => `tab_${Date.now()}_${++tabIdSeq}`;

// mysqlLike / postgresLike 镜像后端 engines.go 的协议族判定: OceanBase(MySQL 模式)/
// GreatSQL/StarRocks/Doris 与 openGauss/GaussDB/金仓在目录命名上与底层引擎一致。
const MYSQL_LIKE: readonly string[] = ['mysql', 'oceanbase', 'greatsql', 'starrocks', 'doris'];

const objectQualifier = (ref: TableRef, dbType?: EngineType) => {
  if (dbType && MYSQL_LIKE.includes(dbType)) return ref.database;
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
  // W16: server mode starts logged out; the first 401 turns this on.
  const [needsLogin, setNeedsLogin] = useState(false);
  const [passcodeDialogOpen, setPasscodeDialogOpen] = useState(false);
  // The idle lock (P2-9) watches the OS and the page; on lock it disconnects
  // everything behind the lock screen, because a live session must not outlive
  // the lock.
  const idleLock = useIdleLock(settings, () => {
    void DisconnectAll().catch(() => {});
    toast('已锁定并断开连接');
  });
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
  // W5: the server-to-server copy dialog. Its source defaults to the connection
  // the navigator was on, which is where a user starts from.
  const [showTransfer, setShowTransfer] = useState(false);
  // W6: the row-level sync dialog.
  const [showRowSync, setShowRowSync] = useState(false);
  // W10: the plugins panel.
  const [showPlugins, setShowPlugins] = useState(false);
  // W-T2: the terminal workbench panel (tabs/split panes/broadcast).
  const [showTerminal, setShowTerminal] = useState(false);
  // W-F1: the files workbench panel (SFTP over the plugin's files.session).
  const [showFiles, setShowFiles] = useState(false);
  // W-M1: the monitor panel (host.stats sampling ring).
  const [showMonitor, setShowMonitor] = useState(false);
  // W-C1: containers over the ssh tunnel (second plugin, depends tunnel.proxy).
  const [showContainers, setShowContainers] = useState(false);
  // W-Q1 / W-N1: the MQTT console (publish gated) and the host-local notes surface.
  const [showMqtt, setShowMqtt] = useState(false);
  const [showNotes, setShowNotes] = useState(false);
  // W-D1: remote desktops (RDP emission with file compensation, VNC channel relay).
  const [showDesktop, setShowDesktop] = useState(false);
  // W-A1/A2: the assistant (host-privileged context, forced SQL preview).
  const [showAI, setShowAI] = useState(false);
  // W-S1: extension catalog + team sync.
  const [showExtensions, setShowExtensions] = useState(false);
  // W17: the plugin console (vector DB / MQ actions declared by a plugin).
  const [showConsole, setShowConsole] = useState(false);
  // W11: the visual execution plan, opened from a plan result's toolbar.
  const [showPlan, setShowPlan] = useState(false);
  // W12: importing connections from another client.
  const [showImportConns, setShowImportConns] = useState(false);
  // W9: the Oracle invalid-object panel.
  const [showObjectHealth, setShowObjectHealth] = useState(false);
  // W15: the about panel (build identity + opt-in update check).
  const [showAbout, setShowAbout] = useState(false);
  // W11b: the SQL notebook panel.
  const [showNotebook, setShowNotebook] = useState(false);
  const [showAiPanel, setShowAiPanel] = useState(false);
  const [confirmReq, setConfirmReq] = useState<ConfirmRequest | null>(null);

  // One confirmation surface for the whole app (P1-16): Safe Mode pauses and
  // the destructive compare-apply used to call window.confirm — unstyled,
  // English, and blocking the webview. This bridges those promise-based callers
  // onto the same ConfirmDialog the sidebar already uses.
  const askConfirm = useCallback(
    (request: { title: string; message: string; confirmLabel: string; destructive?: boolean }) =>
      new Promise<boolean>(resolve => {
        setConfirmReq({
          ...request,
          onConfirm: () => resolve(true),
          onCancel: () => resolve(false),
        });
      }),
    [],
  );

  // The Safe Mode pause answers with a bare message; this is the dialog shape
  // it becomes.
  const confirmDestructive = useCallback(
    (message: string) =>
      askConfirm({ title: '需要确认', message, confirmLabel: '执行', destructive: true }),
    [askConfirm],
  );
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

  // W3-c: two table rows claiming one combo means one handler silently never
  // fires. That is invisible at runtime, so it is asserted once at startup.
  useEffect(() => {
    const dupes = duplicateCombos();
    if (dupes.length > 0) {
      console.error('快捷键声明重复（后者永远不会触发）：', dupes.join(', '));
    }
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
      setNeedsLogin(false);
    } catch (error) {
      // W16: in server mode an unauthenticated call means "log in", not "the app is
      // broken" — showing a toast for it would leave the user staring at an empty shell.
      if (error instanceof AuthRequiredError) {
        setNeedsLogin(true);
        return;
      }
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
      id: newTabId(),
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
    // The backend rolls back and forgets the tab's transaction session;
    // fire-and-forget: tab closing must not block on a network round-trip.
    // A rejected request is logged rather than swallowed: if the server
    // still holds the session, the rollback would otherwise leak silently.
    ForgetTxSession(tabId).catch(e =>
      console.error('Failed to forget transaction session:', e),
    );
    // A statement still running under this tab is cancelled too: the server
    // keeps reading rows (and holding its pooled connection) after the UI has
    // moved on, and for a transaction the rollback lands underneath the still
    // running statement. fire-and-forget, same reason as above.
    const connId = tabs.find(t => t.id === tabId)?.connectionId;
    if (connId) {
      CancelQuery(connId, tabId).catch(e => console.error('Failed to cancel query:', e));
    }
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

  // Column comments of one table are static metadata, and the comments effect
  // re-runs on every tabs change (every keystroke, every streamed batch) while
  // the tab has none: without a cache a comment-less table would refetch
  // GetColumns on each of those. Same shape as commentCache, keyed by object.
  const tableCommentCache = useRef(new Map<string, Promise<Record<string, string>>>());

  const tableComments = (ref: TableRef): Promise<Record<string, string>> => {
    const key = `${ref.connId}|${ref.database}|${ref.schema}|${ref.table}`;
    let pending = tableCommentCache.current.get(key);
    if (!pending) {
      pending = GetColumns(ref.connId, ref.database, ref.schema, ref.table)
        .then(cols => commentMap(cols as unknown as ColumnInfo[]))
        .catch(() => {
          tableCommentCache.current.delete(key);
          return {};
        });
      tableCommentCache.current.set(key, pending);
    }
    return pending;
  };

  // Bumping a tab's run sequence is how "取消等待" makes the in-flight result
  // land nowhere; the server keeps running the statement either way.
  const runSeq = useRef(new Map<string, number>());

  // Interactive transactions (backend txn.go): one per tab. The status the backend
  // returns is the tab's badge; an empty status means autocommit.
  //
  // The badge lags the request, so without a gate a double-click sends the same
  // op twice and the second one fails with "already has an open transaction".
  // txPending records the op in flight per tab; a click while one is pending is
  // ignored and the buttons are disabled for the same reason.
  const [txPending, setTxPending] = useState<Record<string, TxOp | null>>({});
  // Transaction-level isolation override (TXN-03): '' keeps the connection's
  // configured level. Read at Begin so the picker can change between
  // transactions without touching the saved connection.
  const [txIsolation, setTxIsolation] = useState('');
  const txHandlers = (tab: Tab) => ({
    txActive: !!tab.txStatus,
    txStatus: tab.txStatus ?? '',
    txRemainingMs: tab.txRemainingMs ?? 0,
    txWarned: tab.txWarned ?? false,
    txBusy: txBusy(txPending[tab.id] ?? null),
    txIsolation,
    onTxIsolationChange: setTxIsolation,
    onBeginTx: async () => {
      if (!activeConnection) return;
      const pending = txGate(txPending[tab.id] ?? null, 'begin');
      setTxPending(p => ({ ...p, [tab.id]: pending }));
      if (pending !== null) return; // a click while in flight is a no-op
      try {
        // One round trip: the status and the countdown detail arrive together,
        // so a hiccup cannot strand an open transaction behind a missing badge
        // (the calibrator below only re-syncs tabs that already carry one).
        const { status, detail } = await BeginTransaction(activeConnection.id, tab.id, tabDatabase(tab), txIsolation);
        setTabs(current =>
          current.map(t =>
            t.id === tab.id
              ? { ...t, txStatus: status, txRemainingMs: detail.remainingMs, txWarned: detail.warned }
              : t,
          ),
        );
      } catch (error) {
        toast.error((error as Error).message, { duration: 8000 });
      } finally {
        setTxPending(p => ({ ...p, [tab.id]: txSettle(p[tab.id] ?? null) }));
      }
    },
    onCommitTx: async () => {
      if (!activeConnection) return;
      const pending = txGate(txPending[tab.id] ?? null, 'commit');
      setTxPending(p => ({ ...p, [tab.id]: pending }));
      if (pending !== null) return;
      try {
        await CommitTransaction(activeConnection.id, tab.id);
        setTabs(current => current.map(t => (t.id === tab.id ? { ...t, txStatus: '', txRemainingMs: 0, txWarned: false } : t)));
      } catch (error) {
        toast.error((error as Error).message, { duration: 8000 });
      } finally {
        setTxPending(p => ({ ...p, [tab.id]: txSettle(p[tab.id] ?? null) }));
      }
    },
    onRollbackTx: async () => {
      if (!activeConnection) return;
      const pending = txGate(txPending[tab.id] ?? null, 'rollback');
      setTxPending(p => ({ ...p, [tab.id]: pending }));
      if (pending !== null) return;
      try {
        await RollbackTransaction(activeConnection.id, tab.id);
        setTabs(current => current.map(t => (t.id === tab.id ? { ...t, txStatus: '', txRemainingMs: 0, txWarned: false } : t)));
      } catch (error) {
        toast.error((error as Error).message, { duration: 8000 });
      } finally {
        setTxPending(p => ({ ...p, [tab.id]: txSettle(p[tab.id] ?? null) }));
      }
    },
  });

  // Deadline calibration (phase 3): every 30s the open transaction's status
  // is re-read so the local countdown re-syncs with the backend's clock. The
  // backend's sweeper rolls a past-due transaction back on its own — the read
  // then reports autocommit and the badge clears, which is how the UI learns
  // the sweep happened without any push channel.
  useEffect(() => {
    const open = tabs.filter(t => t.txStatus);
    if (open.length === 0) return;
    const timer = setInterval(async () => {
      for (const t of open) {
        try {
          const d = await TransactionStatusDetail(t.id);
          setTabs(current =>
            current.map(tab =>
              tab.id === t.id
                ? tab.txStatus
                  ? { ...tab, txStatus: d.status, txRemainingMs: d.remainingMs, txWarned: d.warned }
                  : tab // the tab closed meanwhile; leave it alone
                : tab,
            ),
          );
        } catch {
          // A failed calibration read is not fatal: the local countdown keeps
          // running, the next tick retries, and the backend sweeps regardless.
        }
      }
    }, 30000);
    return () => clearInterval(timer);
  }, [tabs]);

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

  // The object page's data grid also dies on a reload (page flip, filter edit,
  // refresh) and on a section change, and its edit queue dies with it. Those
  // paths warn the same way a tab switch does, so no door out of the grid is
  // silent about the loss.
  const confirmDiscardPending = (tabId: string, proceed: () => void) => {
    const waiting = pendingEdits.current.get(tabId) ?? 0;
    if (waiting > 0) {
      setConfirmReq({
        title: '丢弃未应用的改动',
        message: `当前网格还有 ${waiting} 处改动没有写入数据库，继续后它们会直接丢掉。`,
        confirmLabel: '丢弃并继续',
        destructive: true,
        onConfirm: proceed,
      });
      return;
    }
    proceed();
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

  // W1-a: 魔法命令（\dt \dv \d \dn \df \di \copy \?）——以 \ 开头的语句被
  // 拦截为结果网格，从不作为 SQL 发往数据库。目录问题走 App 自己的 HTTP
  // API（GetTables/GetTableStructure/GetRoutines），方言与目录保持单点；
  // \copy 的导出直接复用既有流式导出管道。解析与执行拆在 magicCommands.ts
  // （纯函数可单测），这里只做接线。
  const handleMagicCommand = async (tab: Tab, sql: string) => {
    if (!activeConnection) return;
    const database = tabDatabase(tab);
    const tabId = tab.id;
    const connId = activeConnection.id;
    const schemaOf = () => {
      // The tab's database picker holds the schema choice for PG-family
      // engines; MySQL-family uses the database itself.
      return tabDatabase(tab);
    };
    const fetchers = {
      listTables: async () => {
        const list = await GetTables(connId, database, '');
        return list as unknown as { name: string }[];
      },
      listViews: async () => {
        const list = await GetTables(connId, database, '');
        return (list as unknown as { name: string; type?: string }[])
          .filter(x => x.type === 'view');
      },
      listSchemas: async () => {
        if (activeConnection.type === 'sqlite') return [database || 'main'];
        return await GetSchemas(connId, database);
      },
      listRoutines: async () => {
        const routines = await GetRoutines(connId, database, schemaOf());
        return (routines as unknown as { name: string; kind: string }[]) ?? [];
      },
      describe: (table: string) =>
        GetTableStructure(connId, database, '', table) as unknown as Promise<TableStructure>,
      sourceTable: () => tab.tableRef?.table ?? '',
      copyTable: async (table: string, format: string, where: string) => {
        // \copy exports the whole table through the preview endpoint in
        // page-size chunks: it streams the rows the same way a data page
        // reads. A `LIMIT n` tail caps the total; a raw WHERE predicate is
        // refused here (the preview endpoint takes structured filters, not
        // SQL text — free-text WHERE would be a second, ungated SQL path).
        if (where && !/^LIMIT\s+\d+$/i.test(where)) {
          throw new Error('\\copy 仅支持 limit <N> 过滤（结构化筛选请用数据页的筛选条）');
        }
        const cap = where ? Number(where.match(/^LIMIT\s+(\d+)$/i)![1]) : 200000;
        const seen: any[][] = [];
        let names: string[] = [];
        try {
          const structure = (await GetTableStructure(
            connId,
            database,
            '',
            table,
          )) as unknown as TableStructure;
          names = (structure.columns ?? []).map(c => c.name);
        } catch (error) {
          throw new Error(`读取表结构失败: ${(error as Error).message}`);
        }
        const pageSize = 1000;
        for (let offset = 0; offset < cap; offset += pageSize) {
          const page = (await PreviewTable(
            { connId, database, schema: '', table },
            Math.min(pageSize, cap - offset),
            offset,
            [],
            {},
            tabId,
          )) as unknown as QueryResult;
          if (page.error) throw new Error(page.error);
          const rows = (page.rows ?? []) as any[][];
          seen.push(...rows);
          if (rows.length < pageSize) break;
        }
        const cols = names.map(name => ({ name, type: 'text' }));
        const stem = `${database}-${table}`.replace(/[\\/:*?"<>|]+/g, '_');
        if (format === 'csv') {
          download(`${stem}.csv`, 'text/csv;charset=utf-8', toCSV(cols, seen));
        } else if (format === 'tsv') {
          download(`${stem}.tsv`, 'text/tab-separated-values;charset=utf-8', toTSV(cols, seen));
        } else if (format === 'json' || format === 'jsonl') {
          const text = format === 'json'
            ? toJSON(cols, seen)
            : seen.map(row => JSON.stringify(Object.fromEntries(cols.map((c, i) => [c.name, row[i] ?? null])))).join('\n');
          download(`${stem}.${format}`, 'application/json', text);
        } else if (format === 'markdown') {
          download(`${stem}.md`, 'text/markdown;charset=utf-8', toMarkdown(cols, seen));
        } else {
          const workbook = await ExportWorkbook(cols, seen);
          downloadBlob(`${stem}.xlsx`, workbook);
        }
        return seen.length;
      },
    };
    const seq = (runSeq.current.get(tabId) ?? 0) + 1;
    runSeq.current.set(tabId, seq);
    const stillCurrent = () => (runSeq.current.get(tabId) ?? 0) === seq;
    setTabs(current => current.map(t => (t.id === tabId ? { ...t, loading: true } : t)));
    try {
      const parsed = parseMagicCommand(sql);
      if (!parsed) throw new Error('无法解析魔法命令');
      const result = await runMagicCommand(parsed, fetchers);
      if (!stillCurrent()) return;
      setTabs(current => current.map(t =>
        t.id === tabId
          ? { ...t, database, result, comments: undefined, source: null, loading: false }
          : t
      ));
      toast.success(`魔法命令完成${result.note ? ` · ${result.note}` : ''} · ${result.rowCount} 行`);
    } catch (error) {
      if (!stillCurrent()) return;
      const message = (error as Error).message;
      setTabs(current => current.map(t =>
        t.id === tabId
          ? {
              ...t,
              database,
              loading: false,
              result: {
                columns: [], rows: [], rowCount: 0, affectedRows: 0,
                lastInsertId: 0, executionTime: 0, error: message,
              },
            }
          : t
      ));
    }
  };

  const handleExecuteQuery = async (tab: Tab, sqlOverride?: string) => {
    if (!activeConnection) return;
    const database = tabDatabase(tab);
    // The editor passes the statement it actually shows: a named-parameter
    // substitution lands in the editor and this call lands in the same React
    // batch, so tab.query would still be the pre-substitution text.
    //
    // W2-a: in dual mode the statement comes from the ACTIVE pane, and the
    // editor hands over an explicit override from that pane — so an override
    // always belongs to the pane it came from, never to the other one.
    const sql = sqlOverride ?? paneSQL(tab, activePaneOf(tab));
    // W1-a: a `\`-statement is a magic command, never SQL. It is answered by
    // handleMagicCommand and stops here — the database never sees the backslash.
    if (isMagicCommand(sql)) {
      await handleMagicCommand(tab, sql);
      return;
    }
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
      const result = await runStreamed(connId, sql, database, partial => {
        if (!stillCurrent()) return;
        setTabs(current => current.map(t =>
          t.id === tabId ? { ...t, database, result: partial, loading: true } : t
        ));
      }, tabId, !!tab.txStatus, confirmDestructive);
      const comments = settings.showColumnComments && !result.error
        ? await queryComments(activeConnection.id, sql, database)
        : undefined;
      // Which table, if any, the rows can be written back to; a statement that
      // reads from more than one gets null and stays read-only.
      const source = result.error ? null : await GetQuerySource(activeConnection.id, sql, database);
      if (!stillCurrent()) return;

      setTabs(current => current.map(t =>
        t.id === tabId
          ? {
              ...t, database, result, comments, source, loading: false,
              // A keystroke during streaming set isDirty on the live tab;
              // forcing false here would mark edited text as clean. Dirty
              // survives only when the editor text diverged from what ran.
              isDirty: t.isDirty && t.query !== sql,
            }
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
      tableComments(ref).then(comments => {
        if (Object.keys(comments).length) {
          setTabs(current => current.map(t => (t.id === tab.id ? { ...t, comments } : t)));
        }
      });
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
        PreviewTable(ref, pageSize, (page - 1) * pageSize, filters, tab.dataRequest ?? {}, tab.id) as Promise<unknown> as Promise<QueryResult>,
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
      id: newTabId(),
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

  // One more window of a large key's value (P3-3). The shown rows are merged
  // with the new window rather than replacing it; a full reload (which bumps
  // the same sequence) wins over a continuation still in flight.
  const loadKVMore = async (tab: Tab) => {
    const ref = tab.tableRef;
    const shown = tab.kv?.data;
    if (!ref || !shown || !shown.next) return;
    const key = `${tab.id}|kv`;
    const seq = (objectSeq.current.get(key) ?? 0) + 1;
    objectSeq.current.set(key, seq);
    const stillCurrent = () => (objectSeq.current.get(key) ?? 0) === seq;
    const patch = (next: Partial<Tab>) =>
      setTabs(current => current.map(t => (t.id === tab.id ? { ...t, ...next } : t)));

    patch({ kv: { loading: true, data: shown } });
    try {
      const more = (await GetKVValuePage(ref.connId, ref.database, ref.table, shown.next)) as KVValue;
      if (stillCurrent()) {
        // The merge reads the tab's current data inside the updater: two quick
        // clicks must not have the second one build on what the first click
        // saw before its window arrived.
        setTabs(current =>
          current.map(t =>
            t.id === tab.id && t.kv?.data ? { ...t, kv: { loading: false, data: mergeKVWindow(t.kv.data, more) } } : t,
          ),
        );
      }
    } catch (error) {
      if (stillCurrent()) {
        patch({ kv: { loading: false, error: (error as Error).message, data: shown } });
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
      id: newTabId(),
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
    indexes: IndexEditRow[];
    foreignKeys: ForeignKeyEditRow[];
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

  // The database whose "搜索数据" dialog is open (P2-10 whole-db data search).
  const [dataSearchTarget, setDataSearchTarget] = useState<{
    connId: string;
    connName: string;
    database: string;
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
      id: newTabId(),
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
      id: newTabId(),
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
    // Leaving the data section unmounts the grid and its edit queue; ask the
    // same question a tab switch asks when something is still unapplied.
    if ((tab.section ?? 'data') === 'data' && section !== 'data') {
      confirmDiscardPending(tab.id, () => applyObjectSectionChange(tab, section));
      return;
    }
    applyObjectSectionChange(tab, section);
  };

  const applyObjectSectionChange = (tab: Tab, section: ObjectSection) => {
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
    if (section === 'query') return;
    if (section === 'data') {
      confirmDiscardPending(tab.id, () => loadObjectSection(tab, section));
      return;
    }
    loadObjectSection(tab, section);
  };

  // EXPLAIN 只读语句的计划：结果进同一个结果区，但绕过写回源/注释解析——
  // 计划不是行集，也没有可写表。语句本身不执行，Safe Mode 与事务都不参与。
  const handleExplainQuery = async (tab: Tab, sqlOverride?: string) => {
    if (!activeConnection) return;
    const database = tabDatabase(tab);
    const tabId = tab.id;
    const sql = sqlOverride ?? tab.query;
    const seq = (runSeq.current.get(tabId) ?? 0) + 1;
    runSeq.current.set(tabId, seq);
    setTabs(current => current.map(t => (t.id === tabId ? { ...t, loading: true } : t)));
    const stillCurrent = () => (runSeq.current.get(tabId) ?? 0) === seq;
    try {
      const result = await ExplainQuery(activeConnection.id, sql, database);
      if (!stillCurrent()) return;
      // Render the text plan the moment it lands. The structured tree is a
      // second round trip and must not withhold it (NF-2): the grid used to
      // stay empty until the tree came back, so a slow or failing tree call
      // made the whole explain look stuck.
      setTabs(current => current.map(t =>
        t.id === tabId
          ? { ...t, database, result, comments: undefined, source: null, loading: false, plan: undefined }
          : t
      ));
      // W11: the visual tree is best-effort. A failure leaves the text plan in
      // place and says why the tree view is missing.
      const tree = await ExplainPlanTree(activeConnection.id, sql, database).catch(() => null);
      if (!stillCurrent()) return;
      setTabs(current => current.map(t => (t.id === tabId ? { ...t, plan: tree ?? undefined } : t)));
      if (!tree) {
        toast('可视化计划不可用，已显示文本计划');
      }
    } catch (error) {
      if (!stillCurrent()) return;
      toast.error(`执行计划失败：${(error as Error).message}`, { duration: 8000 });
      setTabs(current => current.map(t => (t.id === tabId ? { ...t, loading: false } : t)));
    }
  };

  const handleObjectExecute = async (tab: Tab, sqlOverride?: string) => {
    const ref = tab.tableRef;
    // 同 handleExecuteQuery：命名参数替换落在编辑器里，同批次的闭包读不到。
    const sql = sqlOverride ?? tab.objectQuery?.trim();
    if (!ref || !sql) return;
    // W1-a: magic commands are equally intercepted on the object query page —
    // the tab's source table feeds \d / \di's optional argument here.
    if (isMagicCommand(sql)) {
      await handleMagicCommand(tab, sql);
      return;
    }
    const database = tabDatabase(tab);
    const key = `${tab.id}|query`;
    const seq = (objectSeq.current.get(key) ?? 0) + 1;
    objectSeq.current.set(key, seq);
    const stillCurrent = () => (objectSeq.current.get(key) ?? 0) === seq;
    const patch = (next: Partial<Tab>) =>
      setTabs(current => current.map(t => (t.id === tab.id ? { ...t, ...next } : t)));

    patch({ objectResult: { loading: true }, objectComments: undefined });
    try {
      const result = await runGuarded(ref.connId, sql, database, confirmDestructive, tab.id);
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

  // 对象页的 EXPLAIN：结果进 objectResult（结构页自己的状态位），计划同样
  // 不执行语句、不可写回。语句与主查询页共用同一后端。
  const handleObjectExplainQuery = async (tab: Tab, sqlOverride?: string) => {
    const ref = tab.tableRef;
    const sql = sqlOverride ?? tab.objectQuery?.trim();
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
      const result = await ExplainQuery(ref.connId, sql, database);
      if (stillCurrent()) {
        patch({
          objectResult: { loading: false, data: result },
          objectComments: undefined,
          objectSource: null,
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
      toast.error(`清空历史失败：${(error as Error).message}`, { duration: 8000 });
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
        id: newTabId(),
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
    for (const cache of [tableCache, columnCache, commentCache, tableCommentCache]) {
      for (const key of [...cache.current.keys()]) {
        if (key.split('|')[0] === connId) cache.current.delete(key);
      }
    }
  };

  const objectTabFor = (ref: TableRef) =>
    tabs.find(t => t.kind === 'table' && t.tableRef && objectKey(t.tableRef) === objectKey(ref));

  const handleRenameObject = async (ref: TableRef, newName: string) => {
    await RunTableAction(ref, 'rename', newName, confirmDestructive);
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
    await RunTableAction(ref, 'drop', '', confirmDestructive);
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
    await RunTableAction(ref, 'truncate', '', confirmDestructive);
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
      id: newTabId(),
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
    try {
      await UpdateConnection({ ...connection, password: connection.password ?? '', databaseFilter: filter });
    } catch (error) {
      // The dialog stays open on a failed save: the filter the user typed is
      // still there, and the toast explains why nothing happened.
      toast.error(`保存筛选失败：${(error as Error).message}`, { duration: 8000 });
      throw error;
    }
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
      toast.error(`删除连接失败：${(error as Error).message}`, { duration: 8000 });
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
    // A key/value "object" is a key: opening it must land on the value tab,
    // not on a table tab that would query a table by that name (P1-20).
    if (match.type === 'key') {
      openKVTab(
        { connId: match.connId, database: match.database, schema: '', table: match.table },
        match.table,
      );
      return;
    }
    openTableTab(
      { connId: match.connId, database: match.database, schema: match.schema, table: match.table },
      match.table,
      match.type === 'view' ? 'view' : 'table',
    );
  };

  // App-wide keys, bound here rather than in a widget so they work no matter
  // which panel has the focus.
  //
  // W3-c: the dispatch table is `lib/shortcuts.ts` — the same data the help
  // panel renders, so a shortcut can no longer be documented without being
  // bound (or bound under a name the help does not mention). The combos were
  // duplicated text before; a mismatch was silent.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const combo = normalizeCombo({
        key: event.key,
        ctrlKey: event.ctrlKey,
        metaKey: event.metaKey,
        altKey: event.altKey,
        shiftKey: event.shiftKey,
        isMac: /Mac|iPod|iPhone|iPad/.test(navigator.platform),
      });
      const spec = findShortcut(combo);
      if (!spec) return;
      const run = (fn: () => void) => {
        event.preventDefault();
        fn();
      };
      switch (spec.id) {
        case 'quickOpen':
          run(() => setQuickOpen(current => !current));
          return;
        case 'newTab':
          run(() => handleNewTab());
          return;
        case 'closeTab':
          if (activeTab) run(() => handleCloseTab(activeTab));
          return;
        case 'switchTab': {
          const digit = combo.split('+').pop() ?? '';
          const tab = tabs[Number(digit) - 1];
          if (tab) {
            // Route through switchToTab like a mouse switch: a direct
            // setActiveTab silently discarded unapplied edits on the tab being
            // left, while the same click asked for confirmation.
            run(() => switchToTab(tab.id));
          }
          return;
        }
        case 'help':
        case 'helpAlt':
          run(() => setShowShortcuts(current => !current));
          return;
        case 'privacy': {
          // W8: toggle privacy mode. It is saved like any other setting so the
          // state survives a restart — a user who turned it on for a call and then
          // restarted should not silently start leaking again.
          const next = { ...settings, privacyMode: !settings.privacyMode };
          run(() => {
            setSettings(next);
            saveSettings(next);
            toast.success(next.privacyMode ? '隐私模式已开启' : '隐私模式已关闭');
          });
          return;
        }
        case 'escape':
          // Only the overlays this handler owns are closed here; every dialog
          // also binds its own Escape through useDialog.
          if (quickOpen) run(() => setQuickOpen(false));
          else if (showShortcuts) run(() => setShowShortcuts(false));
          return;
        default:
          // Editor-scoped shortcuts are bound in the editor's own keymap; the
          // table lists them for the help panel only.
          return;
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabs, activeTab, quickOpen, showShortcuts]);

  return (
    <div className={`flex h-screen bg-canvas text-ink${settings.privacyMode ? ' privacy' : ''}`}>
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
        onSearchData={node =>
          setDataSearchTarget({
            connId: node.connId,
            connName: connections.find(c => c.id === node.connId)?.name ?? '',
            database: (node.database as string) ?? node.label,
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
                title={privateTitle(fullName, settings.privacyMode)}
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
              onClick={() => setShowTransfer(true)}
              title="数据传输：把表结构和数据复制到另一个连接"
              aria-label="数据传输"
            >
              <FiShare2 size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowRowSync(true)}
              title="行级同步：按主键把两张同名表的行对齐"
              aria-label="行级数据同步"
            >
              <FiRefreshCw size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowPlugins(true)}
              title="插件：查看/信任插件驱动"
              aria-label="插件"
            >
              <FiShield size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowImportConns(true)}
              title="导入连接：从 DBeaver / URL 列表 / CSV"
              aria-label="导入连接"
            >
              <FiDownload size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowObjectHealth(true)}
              title="无效对象：列出并重编译（Oracle）"
              aria-label="无效对象"
            >
              <FiCode size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowConsole(true)}
              title="控制台：执行插件声明的向量库/MQ 动作"
              aria-label="插件控制台"
            >
              <FiTerminal size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowTerminal(true)}
              title="终端：SSH 会话、分屏与广播（navop-ssh 插件）"
              aria-label="终端工作台"
            >
              <FiCode size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowFiles(true)}
              title="文件：SFTP 浏览/上传/下载（navop-ssh 插件）"
              aria-label="文件工作台"
            >
              <FiDownload size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowMonitor(true)}
              title="监控：host.stats 采样环（定时器为效应，卸载即停）"
              aria-label="监控工作台"
            >
              <FiActivity size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowContainers(true)}
              title="容器：经 SSH 隧道访问 docker（navop-containers 依赖 tunnel.proxy）"
              aria-label="容器工作台"
            >
              <FiBox size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowMqtt(true)}
              title="消息：MQTT 控制台（发布是发射，受 Safe Mode 扣留；订阅是获取）"
              aria-label="消息工作台"
            >
              <FiMessageSquare size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowNotes(true)}
              title="笔记：宿主本地文档（挂 nav.nodes）"
              aria-label="笔记"
            >
              <FiFileText size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowDesktop(true)}
              title="远程桌面：RDP（发射 + .rdp 清理逆）与 VNC 通道"
              aria-label="远程桌面"
            >
              <FiMonitor size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowAI(true)}
              title="AI 提供商：上下文由宿主附加，SQL 只预览不执行"
              aria-label="AI 助手"
            >
              <FiCpu size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowExtensions(true)}
              title="扩展：目录安装（摘要校验）+ 团队同步（秘密为 ref）"
              aria-label="扩展"
            >
              <FiPackage size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowAbout(true)}
              title="关于：版本与构建信息"
              aria-label="关于"
            >
              <FiInfo size={14} />
            </button>
            <button
              className="p-1.5 text-secondary hover:text-ink"
              onClick={() => setShowNotebook(true)}
              title="笔记本：可互相引用的 SQL 单元格"
              aria-label="笔记本"
            >
              <FiBookOpen size={14} />
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
              confirm={askConfirm}
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
                confirmDiscardPending(activeTabData.id, () =>
                  showDataPage(activeTabData, page, activeTabData.dataPageSize ?? DEFAULT_PAGE_SIZE)
                )
              }
              onDataPageSize={size =>
                confirmDiscardPending(activeTabData.id, () => showDataPage(activeTabData, 1, size))
              }
              dataFilters={activeTabData.dataFilters ?? []}
              onDataFilters={filters =>
                confirmDiscardPending(activeTabData.id, () => showDataFilters(activeTabData, filters))
              }
              structure={activeTabData.structure ?? { loading: false }}
              history={activeTabData.history ?? { loading: false }}
              comments={activeTabData.comments}
              showComments={settings.showColumnComments}
              editSource={activeTabData.objectType === 'view' ? null : objectRef}
              exportName={fileStem([objectRef.database, objectRef.table], settings.privacyMode)}
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
                  indexes: indexEditRows(activeTabData.structure.data.indexes),
                  foreignKeys: foreignKeyEditRows(activeTabData.structure.data.foreignKeys),
                });
              }}
              onImport={() => setImportTarget(objectRef)}
              onRefresh={() => handleObjectRefresh(activeTabData)}
              onDataApplied={() => loadObjectSection(activeTabData, 'data')}
              onPendingChange={total => pendingEdits.current.set(activeTabData.id, total)}
              onDismissError={section => clearObjectSection(activeTabData.id, section)}
              onUseStatement={sql => handleUseStatement(activeTabData, sql)}
              onClearHistory={() => handleClearHistory(activeTabData)}
              confirm={askConfirm}
              queryPage={
                <ObjectQueryPage
                  sql={activeTabData.objectQuery ?? ''}
                  onSqlChange={sql =>
                    setTabs(current =>
                      current.map(t => (t.id === activeTabData.id ? { ...t, objectQuery: sql } : t))
                    )
                  }
                  onExecute={sql => handleObjectExecute(activeTabData, sql)}
                  onExplain={sql => handleObjectExplainQuery(activeTabData, sql)}
                  onCancel={() => {
                    if (!activeConnection) return;
                    CancelQuery(activeConnection.id, activeTabData.id).catch(() => {});
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
                  exportName={fileStem([objectRef.database, objectRef.table], settings.privacyMode)}
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
              onLoadMore={
                activeTabData.kv?.data?.next && !activeTabData.kv?.data?.done
                  ? () => loadKVMore(activeTabData)
                  : undefined
              }
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
                    // Functional form: a keystroke landing in the same batch as
                    // a streaming partial result must build on that result's
                    // tab state, not on the stale snapshot this closure holds.
                    setTabs(current => current.map(tab =>
                      tab.id === activeTab
                        ? { ...tab, query, isDirty: true }
                        : tab
                    ));
                  }}
                  onExecute={sql => handleExecuteQuery(activeTabData, sql)}
                  onExplain={sql => handleExplainQuery(activeTabData, sql)}
                  onCancel={() => {
                    if (!activeConnection) return;
                    // Stop the server-side statement, then clear the spinner:
                    // bumping the tab's sequence makes the trailing stream
                    // events no-ops, so the rows already painted stay visible.
                    CancelQuery(activeConnection.id, activeTabData.id).catch(() => {});
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
                  passcodeSet={idleLock.passcodeSet}
                  onManagePasscode={() => setPasscodeDialogOpen(true)}
                  getTables={completionTables}
                  getColumns={completionColumns}
                  // W2-a: the dual-pane editor's state lives on the tab, so a
                  // tab switch preserves both statements and which pane ran.
                  dualPane={activeTabData.dualPane}
                  activePane={activeTabData.activePane ?? 1}
                  query2={activeTabData.query2}
                  onQuery2Change={sql =>
                    setTabs(current => current.map(tab =>
                      tab.id === activeTab ? { ...tab, query2: sql, isDirty: true } : tab
                    ))
                  }
                  onActivePaneChange={pane => setTabs(current => current.map(tab =>
                    tab.id === activeTab && tab.dualPane ? { ...tab, activePane: pane } : tab
                  ))}
                  onToggleDualPane={() => setTabs(current => current.map(tab =>
                    tab.id === activeTab ? toggleDualPane(tab) : tab
                  ))}
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
                  {/* W11: a plan result gets a visual entry beside the grid. The
                      grid stays the default view (it holds the raw text and the
                      per-engine columns); the graph is the same data, drawn. */}
                  {activeTabData.plan && (
                    <div className="flex items-center gap-2 px-3 py-1 bg-panel border-b border-line shrink-0">
                      <span className="text-2xs text-muted">
                        执行计划 · {activeTabData.plan.nodes} 个节点
                      </span>
                      <button
                        className="px-2 py-0.5 rounded text-2xs border border-line text-secondary hover:text-ink hover:bg-raised"
                        onClick={() => setShowPlan(true)}
                      >
                        可视化
                      </button>
                    </div>
                  )}
                  <DataGrid
                    result={activeTabData.result}
                    comments={activeTabData.comments}
                    showComments={settings.showColumnComments}
                    editSource={activeTabData.source}
                    onApplied={() => handleExecuteQuery(activeTabData)}
                    onPendingChange={total => pendingEdits.current.set(activeTabData.id, total)}
                    onDismissError={() => clearResult(activeTabData.id)}
                    exportName={fileStem([tabDatabase(activeTabData), activeTabData.title], settings.privacyMode)}
                  />
                </div>
              )}
            </>
          ) : (
            <div className="flex-1 flex items-center justify-center text-muted">
              <div className="text-center">
                <p className="text-2xl mb-2">TablePro Go</p>
                <p className="text-sm">新建一个查询标签页开始使用</p>
              </div>
            </div>
          )}
        </div>
      </div>

      {showAiPanel && <AiPanel onClose={() => setShowAiPanel(false)} confirm={askConfirm} />}

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

      {dataSearchTarget && (
        <DataSearchDialog target={dataSearchTarget} onClose={() => setDataSearchTarget(null)} />
      )}

      {structureEditTarget && (
        <StructureEditorDialog
          target={structureEditTarget.ref}
          columns={structureEditTarget.columns}
          indexes={structureEditTarget.indexes}
          foreignKeys={structureEditTarget.foreignKeys}
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

      {showTransfer && (
        <TransferDialog
          connections={connections}
          connectedIds={connectedIds}
          initialSource={
            activeConnection
              ? {
                  connId: activeConnection.id,
                  database: tabDatabase(activeTabData),
                  schema: '',
                }
              : undefined
          }
          confirm={askConfirm}
          onClose={() => setShowTransfer(false)}
        />
      )}

      {showRowSync && (
        <RowSyncDialog
          connections={connections}
          connectedIds={connectedIds}
          initialSource={
            activeConnection
              ? {
                  connId: activeConnection.id,
                  database: tabDatabase(activeTabData),
                  schema: '',
                }
              : undefined
          }
          confirm={askConfirm}
          onClose={() => setShowRowSync(false)}
        />
      )}

      {showPlugins && <PluginPanel onClose={() => setShowPlugins(false)} />}

      {showImportConns && (
        <ImportConnectionsDialog
          onClose={() => setShowImportConns(false)}
          onImported={() => void loadConnections()}
        />
      )}

      {showAbout && <AboutDialog onClose={() => setShowAbout(false)} />}

      {showNotebook && activeConnection && (
        <NotebookPanel
          connId={activeConnection.id}
          database={tabDatabase(activeTabData)}
          onClose={() => setShowNotebook(false)}
        />
      )}

      {showObjectHealth && activeConnection && (
        <ObjectHealthDialog
          connId={activeConnection.id}
          database={tabDatabase(activeTabData)}
          schema=""
          onClose={() => setShowObjectHealth(false)}
        />
      )}

      {showConsole && activeConnection && (
        <PluginConsole
          connId={activeConnection.id}
          connName={activeConnection.name}
          onClose={() => setShowConsole(false)}
        />
      )}

      {showTerminal && (
        <TerminalPanel onClose={() => setShowTerminal(false)} />
      )}

      {showFiles && (
        <FilesPanel onClose={() => setShowFiles(false)} />
      )}

      {showMonitor && (
        <MonitorPanel onClose={() => setShowMonitor(false)} />
      )}

      {showContainers && (
        <ContainersPanel onClose={() => setShowContainers(false)} />
      )}

      {showMqtt && (
        <MqttPanel onClose={() => setShowMqtt(false)} />
      )}

      {showNotes && (
        <NotesPanel onClose={() => setShowNotes(false)} />
      )}

      {showDesktop && (
        <DesktopPanel onClose={() => setShowDesktop(false)} />
      )}

      {showAI && (
        <AiProviderPanel onClose={() => setShowAI(false)} />
      )}

      {showExtensions && (
        <ExtensionsPanel onClose={() => setShowExtensions(false)} />
      )}

      {showPlan && activeTabData?.plan && (
        <ExplainPlanView plan={activeTabData.plan} onClose={() => setShowPlan(false)} />
      )}

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

      {passcodeDialogOpen && (
        <PasscodeDialog
          passcodeSet={idleLock.passcodeSet}
          onClose={() => setPasscodeDialogOpen(false)}
          onChanged={() => {
            void idleLock.refreshPasscode();
          }}
        />
      )}

      {idleLock.locked && (
        <LockScreen
          reason={idleLock.reason}
          idleLockMinutes={settings.idleLockMinutes}
          onUnlock={idleLock.unlock}
        />
      )}

      <Toaster position="bottom-right" />
    </div>
  );
}

export default App;

