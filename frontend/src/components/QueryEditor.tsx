import React, { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import { Compartment, EditorState } from '@codemirror/state';
import {
  EditorView,
  crosshairCursor,
  drawSelection,
  dropCursor,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  rectangularSelection,
} from '@codemirror/view';
import { defaultKeymap, historyKeymap, history, indentWithTab } from '@codemirror/commands';
import { FiAlignLeft, FiStar, FiTrash2, FiX } from 'react-icons/fi';
import {
  HighlightStyle,
  bracketMatching,
  foldGutter,
  indentOnInput,
  syntaxHighlighting,
  defaultHighlightStyle,
} from '@codemirror/language';
import {
  Completion,
  CompletionContext,
  CompletionResult,
  autocompletion,
  closeBrackets,
  closeBracketsKeymap,
  completionKeymap,
} from '@codemirror/autocomplete';
import {
  MSSQL,
  MySQL,
  PLSQL,
  PostgreSQL,
  SQLDialect,
  SQLite,
  keywordCompletionSource,
  sql,
} from '@codemirror/lang-sql';
import { tags } from '@lezer/highlight';
import {
  FiCheck,
  FiChevronDown,
  FiDatabase,
  FiGitCommit,
  FiLoader,
  FiLock,
  FiMinus,
  FiPlay,
  FiPlus,
  FiRotateCcw,
  FiSettings,
  FiSquare,
} from 'react-icons/fi';
import toast from 'react-hot-toast';
import { AddFavorite, AppSettings, DeleteFavorite,
  Favorite, FormatSQL, ListFavorites,
} from '../api';
import { ColumnInfo } from '../types';
import { findNamedParams, substituteParams } from '../lib/namedParams';

export interface QueryEditorHandle {
  insertAtCursor: (text: string) => void;
  focus: () => void;
}

interface QueryEditorProps {
  query: string;
  onQueryChange: (query: string) => void;
  onExecute: () => void;
  // onCancel aborts the in-flight statement server-side (cancels its context).
  // Optional so read-only embeds (object query page) can omit it.
  onCancel?: () => void;
  isConnected: boolean;
  loading: boolean;
  // Interactive transaction (backend txn.go): the tab either runs in autocommit or
  // holds one open transaction, and only one is allowed at a time.
  txStatus?: string;
  txActive?: boolean;
  onBeginTx?: () => void;
  onCommitTx?: () => void;
  onRollbackTx?: () => void;
  dialect: string;
  // Databases the tab can target. An empty list hides the picker (sqlite).
  databases: string[];
  database: string;
  onDatabaseChange: (database: string) => void;
  settings: AppSettings;
  onSettingsChange: (settings: AppSettings) => void;
  // P2-9: whether a local passcode is stored, and how to open its manager.
  // Optional: the object-query editor shares this component but is not the
  // settings surface, so it simply does not pass them.
  passcodeSet?: boolean;
  onManagePasscode?: () => void;
  getTables: (database?: string) => Promise<string[]>;
  getColumns: (table: string, database?: string) => Promise<ColumnInfo[]>;
}

const dialects: Record<string, SQLDialect> = {
  mysql: MySQL,
  postgresql: PostgreSQL,
  sqlserver: MSSQL,
  sqlite: SQLite,
  oracle: PLSQL,
};

// The editor reads the app's palette through CSS variables, so switching the
// theme repaints it without rebuilding any extension.
const editorTheme = EditorView.theme({
  '&': { color: 'rgb(var(--ink))', backgroundColor: 'rgb(var(--canvas))', height: '100%' },
  '.cm-content': { caretColor: 'rgb(var(--accent))' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'rgb(var(--accent))' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
    backgroundColor: 'rgb(var(--accent) / 0.28)',
  },
  '.cm-activeLine': { backgroundColor: 'rgb(var(--panel) / 0.7)' },
  '.cm-gutters': {
    backgroundColor: 'rgb(var(--canvas))',
    color: 'rgb(var(--muted))',
    border: 'none',
    borderRight: '1px solid rgb(var(--line))',
  },
  '.cm-activeLineGutter': { backgroundColor: 'rgb(var(--panel))', color: 'rgb(var(--secondary))' },
  '.cm-foldGutter span': { color: 'rgb(var(--muted))' },
  '.cm-tooltip': {
    backgroundColor: 'rgb(var(--panel))',
    border: '1px solid rgb(var(--line-strong))',
    color: 'rgb(var(--ink))',
  },
  '.cm-tooltip.cm-tooltip-autocomplete > ul': { fontFamily: 'inherit' },
  '.cm-tooltip.cm-tooltip-autocomplete > ul > li': { padding: '2px 6px' },
  '.cm-tooltip-autocomplete ul li[aria-selected]': {
    backgroundColor: 'rgb(var(--accent))',
    color: 'rgb(var(--on-accent))',
  },
  '.cm-completionDetail': { color: 'rgb(var(--muted))', fontStyle: 'normal', marginLeft: '8px' },
  '.cm-tooltip.cm-tooltip-info': { maxWidth: '24rem', whiteSpace: 'pre-wrap' },
});

const sqlStyle = HighlightStyle.define([
  { tag: tags.keyword, color: 'rgb(var(--accent))' },
  { tag: tags.operatorKeyword, color: 'rgb(var(--accent))' },
  { tag: tags.controlKeyword, color: 'rgb(var(--syn-control))' },
  { tag: tags.comment, color: 'rgb(var(--muted))', fontStyle: 'italic' },
  { tag: tags.lineComment, color: 'rgb(var(--muted))', fontStyle: 'italic' },
  { tag: tags.blockComment, color: 'rgb(var(--muted))', fontStyle: 'italic' },
  { tag: tags.string, color: 'rgb(var(--syn-string))' },
  { tag: tags.number, color: 'rgb(var(--syn-number))' },
  { tag: tags.bool, color: 'rgb(var(--syn-number))' },
  { tag: tags.null, color: 'rgb(var(--syn-number))' },
  { tag: tags.typeName, color: 'rgb(var(--syn-type))' },
  { tag: tags.propertyName, color: 'rgb(var(--ink))' },
  { tag: tags.variableName, color: 'rgb(var(--ink))' },
]);

const quotedName = /[[\]"`]/g;

interface Mention {
  database: string;
  table: string;
}

// "APPEnv.dbo.svn_lock" -> { database: 'APPEnv', table: 'svn_lock' }
const parseMention = (raw: string): Mention => {
  const parts = raw.replace(quotedName, '').split('.').filter(Boolean);
  return {
    database: parts.length >= 3 ? parts[parts.length - 3] : '',
    table: parts[parts.length - 1] ?? '',
  };
};

const tableMentions = (doc: string): Mention[] =>
  [...doc.matchAll(/\b(?:from|join|update|into)\s+([[\]".`\w$-]+)/gi)]
    .map(m => parseMention(m[1]))
    .filter(m => m.table);

// Completes the tables of the database the statement points at, plus the
// columns of every table it mentions; column comments ride along as detail.
function makeCompleter(
  getTables: (database?: string) => Promise<string[]>,
  getColumns: (table: string, database?: string) => Promise<ColumnInfo[]>,
) {
  const columnCache = new Map<string, Completion[]>();

  const columnsFor = async (mention: Mention): Promise<Completion[]> => {
    const key = `${mention.database}|${mention.table.toLowerCase()}`;
    const cached = columnCache.get(key);
    if (cached) return cached;

    const built = (await getColumns(mention.table, mention.database || undefined)).map(c => ({
      label: c.name,
      type: 'column',
      detail: c.type,
      info: c.comment || undefined,
      boost: c.isPrimaryKey ? 1 : 0,
    }));
    columnCache.set(key, built);
    return built;
  };

  return async (cx: CompletionContext): Promise<CompletionResult | null> => {
    const word = cx.matchBefore(/[\w$]*/);
    if (!word || (word.from === word.to && !cx.explicit)) return null;

    const doc = cx.state.doc.toString();
    const mentions = Array.from(
      new Map(tableMentions(doc).map(m => [`${m.database}|${m.table.toLowerCase()}`, m])).values(),
    );

    const loaded = await Promise.all(mentions.map(m => columnsFor(m).catch(() => [] as Completion[])));
    const columns = Array.from(new Map(loaded.flat().map(c => [c.label, c])).values());

    const hint = mentions.find(m => m.database)?.database;

    const afterDot = /[[\]".`\w$]*\.\s*$/.test(cx.state.doc.sliceString(0, word.from).slice(-80));
    if (afterDot) {
      const tables = mentions.map(m => ({ label: m.table, type: 'table' }));
      return { from: word.from, options: [...tables, ...columns], validFor: /^[\w$]*$/ };
    }

    let tables: Completion[] = [];
    try {
      tables = (await getTables(hint)).map(t => ({ label: t, type: 'table', boost: 2 }));
    } catch {
      // Not connected yet: columns already known and keywords are still useful.
    }
    return { from: word.from, options: [...tables, ...columns], validFor: /^[\w$]*$/ };
  };
}

const QueryEditor = forwardRef<QueryEditorHandle, QueryEditorProps>(function QueryEditor(
  {
    query,
    onQueryChange,
    onExecute,
    onCancel,
    isConnected,
    loading,
    txStatus = '',
    txActive = false,
    onBeginTx,
    onCommitTx,
    onRollbackTx,
    dialect,
    databases,
    database,
    onDatabaseChange,
    settings,
    onSettingsChange,
    passcodeSet,
    onManagePasscode,
    getTables,
    getColumns,
  },
  ref,
) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const lastEmitted = useRef(query);
  const [showSettings, setShowSettings] = React.useState(false);
  // Favorites: the curated counterpart of history. Loaded when the menu opens,
  // so a favorite saved in another tab shows up without a refresh.
  const [favorites, setFavorites] = useState<Favorite[]>([]);
  const [showFavorites, setShowFavorites] = useState(false);

  // Named parameters: a statement with `:name` markers asks for values before
  // it runs. The substitution lands in the editor itself, so what runs is
  // exactly what the user sees — the same contract a typed statement has.
  const [paramDialog, setParamDialog] = useState<{ names: string[]; values: Record<string, string> } | null>(null);

  const executeWithParams = () => {
    const sql = viewRef.current?.state.doc.toString() ?? '';
    const names = findNamedParams(sql);
    if (names.length === 0) {
      onExecute();
      return;
    }
    setParamDialog({ names, values: {} });
  };

  const confirmParams = () => {
    if (!paramDialog) return;
    const view = viewRef.current;
    if (!view) return;
    const sql = view.state.doc.toString();
    const filled = substituteParams(sql, paramDialog.values);
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: filled },
      selection: { anchor: Math.min(view.state.selection.main.head, filled.length) },
    });
    setParamDialog(null);
    onExecute();
  };

  const refreshFavorites = () => {
    ListFavorites()
      .then(list => setFavorites(list))
      .catch(error => toast.error(`收藏加载失败：${(error as Error).message}`));
  };

  const saveFavorite = async () => {
    const sql = viewRef.current?.state.doc.toString().trim() ?? '';
    if (!sql) {
      toast.error('编辑器为空，没有可收藏的语句');
      return;
    }
    const name = window.prompt('收藏名称', sql.split('\n')[0].slice(0, 50));
    if (!name) return;
    try {
      await AddFavorite('', '', name.trim(), sql);
      toast.success('已收藏');
      refreshFavorites();
    } catch (error) {
      toast.error(`收藏失败：${(error as Error).message}`);
    }
  };

  const removeFavorite = async (id: string) => {
    try {
      await DeleteFavorite(id);
      refreshFavorites();
    } catch (error) {
      toast.error(`删除收藏失败：${(error as Error).message}`);
    }
  };

  // Callbacks change on every parent render; routing them through a ref lets
  // the editor instance be created once.
  const handlers = useRef({ onQueryChange, onExecute, getTables, getColumns, loading });
  handlers.current = { onQueryChange, onExecute, getTables, getColumns, loading };
  const executeGate = useRef<() => void>(() => {});
  executeGate.current = executeWithParams;

  const fontCompartment = useMemo(() => new Compartment(), []);
  const dialectCompartment = useMemo(() => new Compartment(), []);
  const completionCompartment = useMemo(() => new Compartment(), []);
  const completerRef = useRef<ReturnType<typeof makeCompleter> | null>(null);

  // The editor font size is a per-user preference, not per-tab state: every
  // tab reads the same saved value, and +/- persists the new one.
  const savedFontSize = (): number => {
    try {
      const n = Number(localStorage.getItem('tablepro.editorFontSize'));
      if (n >= 10 && n <= 22) return n;
    } catch { /* unavailable storage falls back to the default */ }
    return 13;
  };

  const fontSizeTheme = (size: number) =>
    EditorView.theme({ '&': { fontSize: `${size}px` }, '.cm-scroller': { lineHeight: '1.5' } });

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    const completer = makeCompleter(
      db => handlers.current.getTables(db),
      (table, db) => handlers.current.getColumns(table, db),
    );
    completerRef.current = completer;

    const view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: lastEmitted.current,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightSpecialChars(),
          history(),
          foldGutter(),
          drawSelection(),
          dropCursor(),
          EditorState.allowMultipleSelections.of(true),
          indentOnInput(),
          bracketMatching(),
          closeBrackets(),
          completionCompartment.of(
            autocompletion({ override: [keywordCompletionSource(MySQL, true), completer] }),
          ),
          rectangularSelection(),
          crosshairCursor(),
          highlightActiveLine(),
          syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
          syntaxHighlighting(sqlStyle),
          keymap.of([
            {
              key: 'Ctrl-Enter',
              mac: 'Cmd-Enter',
              run: () => {
                // A second run while the first is in flight only piles up
                // connections against the server.
                if (!handlers.current.loading) executeGate.current();
                return true;
              },
            },
            {
              key: 'Ctrl-Shift-f',
              mac: 'Cmd-Shift-f',
              run: () => {
                void formatSQL();
                return true;
              },
            },
            ...closeBracketsKeymap,
            ...defaultKeymap,
            ...historyKeymap,
            ...completionKeymap,
            indentWithTab,
          ]),
          EditorView.lineWrapping,
          editorTheme,
          fontCompartment.of(fontSizeTheme(savedFontSize())),
          dialectCompartment.of(sql({ dialect: MySQL, upperCaseKeywords: true })),
          EditorView.updateListener.of(update => {
            if (!update.docChanged) return;
            const value = update.state.doc.toString();
            lastEmitted.current = value;
            handlers.current.onQueryChange(value);
          }),
        ],
      }),
    });

    viewRef.current = view;
    return () => {
      view.destroy();
      viewRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const view = viewRef.current;
    if (view && query !== lastEmitted.current) {
      lastEmitted.current = query;
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: query } });
    }
  }, [query]);

  // Keywords and the dialect grammar both follow the connected engine.
  useEffect(() => {
    const view = viewRef.current;
    if (!view || !completerRef.current) return;
    const active = dialects[dialect] ?? MySQL;
    view.dispatch({
      effects: [
        dialectCompartment.reconfigure(sql({ dialect: active, upperCaseKeywords: true })),
        completionCompartment.reconfigure(
          autocompletion({
            override: [keywordCompletionSource(active, true), completerRef.current],
          }),
        ),
      ],
    });
  }, [dialect, dialectCompartment, completionCompartment]);

  useImperativeHandle(ref, () => ({
    insertAtCursor: (text: string) => {
      const view = viewRef.current;
      if (!view) return;
      const { from, to } = view.state.selection.main;
      view.dispatch({
        changes: { from, to, insert: text },
        selection: { anchor: from + text.length },
        scrollIntoView: true,
      });
      view.focus();
    },
    focus: () => viewRef.current?.focus(),
  }));

  const formatSQL = async () => {
    const view = viewRef.current;
    if (!view) return;
    try {
      const formatted = await FormatSQL(view.state.doc.toString());
      if (!formatted) return;
      // Replace the whole document but keep the cursor where it was, clamped.
      const pos = Math.min(view.state.selection.main.head, formatted.length);
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: formatted },
        selection: { anchor: pos },
      });
    } catch (error) {
      toast.error(`格式化失败：${(error as Error).message}`);
    }
  };

  const adjustFont = (delta: number) => {
    const view = viewRef.current;
    if (!view) return;
    const current = parseFloat(getComputedStyle(view.dom).fontSize) || 13;
    const next = Math.min(22, Math.max(10, current + delta));
    view.dispatch({ effects: fontCompartment.reconfigure(fontSizeTheme(next)) });
    try {
      localStorage.setItem('tablepro.editorFontSize', String(next));
    } catch { /* private mode: the choice just does not survive */ }
  };

  // A tab may target a database other than the connection's own so table names
  // need no prefix; keep the current choice visible even if it is not in the
  // loaded list yet.
  const databaseOptions =
    database && !databases.includes(database) ? [database, ...databases] : databases;

  return (
    <div className="flex flex-col min-h-0 h-full">
      <div className="flex items-center px-2 py-1.5 bg-panel border-b border-line gap-2 shrink-0">
        {databaseOptions.length > 0 && (
          <div className="relative shrink-0">
            <FiDatabase
              size={11}
              className="absolute left-2 top-1/2 -translate-y-1/2 text-muted pointer-events-none"
            />
            <FiChevronDown
              size={11}
              className="absolute right-1.5 top-1/2 -translate-y-1/2 text-muted pointer-events-none"
            />
            <select
              className="appearance-none bg-canvas hover:bg-raised border border-line rounded pl-6 pr-6 py-1 text-xs text-ink max-w-[200px] cursor-pointer"
              value={database}
              onChange={e => onDatabaseChange(e.target.value)}
              aria-label="查询使用的数据库"
              title="查询使用的数据库：表名无需再加库前缀"
            >
              {databaseOptions.map(db => (
                <option key={db} value={db}>
                  {db}
                </option>
              ))}
            </select>
          </div>
        )}

        <button
          className={`flex items-center whitespace-nowrap px-3 py-1 rounded text-xs font-medium ${
            isConnected && !loading
              ? 'bg-success hover:bg-success-hover text-on-success'
              : 'bg-raised text-muted cursor-not-allowed'
          }`}
          onClick={executeWithParams}
          disabled={!isConnected || loading}
          title="执行 (Ctrl+Enter)"
        >
          {loading ? (
            <FiLoader size={12} className="mr-1.5 shrink-0 animate-spin" />
          ) : (
            <FiPlay size={12} className="mr-1.5 shrink-0" />
          )}
          {loading ? '执行中' : '执行'}
        </button>

        {loading && (
          <button
            className="flex items-center whitespace-nowrap px-3 py-1 rounded text-xs font-medium bg-danger hover:bg-danger-hover text-on-danger"
            onClick={onCancel ?? (() => {})}
            title="取消当前语句（服务端中止）"
          >
            <FiSquare size={11} className="mr-1.5 shrink-0" />
            取消
          </button>
        )}

        {isConnected && (
          <div className="flex items-center gap-1 pl-2 border-l border-line">
            {txActive ? (
              <>
                <button
                  className="flex items-center whitespace-nowrap px-2 py-1 rounded text-2xs bg-success hover:bg-success-hover text-on-success disabled:opacity-40"
                  onClick={onCommitTx}
                  disabled={loading}
                  title="提交这个标签页的事务"
                >
                  <FiCheck size={11} className="mr-1 shrink-0" />
                  提交
                </button>
                <button
                  className="flex items-center whitespace-nowrap px-2 py-1 rounded text-2xs bg-danger hover:bg-danger-hover text-on-danger disabled:opacity-40"
                  onClick={onRollbackTx}
                  disabled={loading}
                  title="回滚这个标签页的事务，撤掉其中所有语句"
                >
                  <FiRotateCcw size={11} className="mr-1 shrink-0" />
                  回滚
                </button>
              </>
            ) : (
              <button
                className="flex items-center whitespace-nowrap px-2 py-1 rounded text-2xs bg-raised text-secondary hover:text-ink disabled:opacity-40"
                onClick={onBeginTx}
                disabled={loading}
                title="开始事务：之后的语句都在事务里，直到提交或回滚"
              >
                <FiGitCommit size={11} className="mr-1 shrink-0" />
                开始事务
              </button>
            )}
            {txStatus && (
              <span className="text-2xs text-warning whitespace-nowrap" title={txStatus}>
                {txStatus}
              </span>
            )}
          </div>
        )}

        <span className="text-2xs text-muted whitespace-nowrap">
          {isConnected ? 'Ctrl+Enter 执行 · Ctrl+Space 补全关键字/表/列' : '尚未连接数据库'}
        </span>

        <div className="ml-auto flex items-center gap-1">
          <div className="relative">
            <button
              className={`flex items-center justify-center w-6 h-6 rounded hover:bg-raised ${
                showFavorites ? 'text-accent' : 'text-secondary hover:text-ink'
              }`}
              onClick={() => {
                if (!showFavorites) refreshFavorites();
                setShowFavorites(v => !v);
              }}
              aria-label="查询收藏"
              aria-expanded={showFavorites}
              title="查询收藏"
            >
              <FiStar size={12} />
            </button>

            {showFavorites && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setShowFavorites(false)} />
                <div className="absolute right-0 top-7 z-50 w-72 max-h-80 overflow-auto p-1 bg-panel border border-line-strong rounded shadow-xl">
                  <button
                    className="w-full text-left px-2 py-1.5 text-xs text-accent hover:bg-raised rounded"
                    onClick={() => {
                      void saveFavorite();
                      setShowFavorites(false);
                    }}
                  >
                    ☆ 收藏当前语句
                  </button>
                  {favorites.length === 0 && (
                    <div className="px-2 py-1.5 text-2xs text-muted">还没有收藏，编辑好语句后点上面收藏</div>
                  )}
                  {favorites.map(f => (
                    <div key={f.id} className="group flex items-center gap-1 px-1 py-0.5 rounded hover:bg-raised">
                      <button
                        className="flex-1 min-w-0 text-left px-1 py-1 rounded"
                        onClick={() => {
                          viewRef.current?.dispatch({
                            changes: { from: 0, to: viewRef.current.state.doc.length, insert: f.sql },
                          });
                          setShowFavorites(false);
                        }}
                        title={f.sql}
                      >
                        <span className="block text-xs text-ink truncate">{f.name}</span>
                        <span className="block text-2xs text-muted truncate">{f.sql}</span>
                      </button>
                      <button
                        className="shrink-0 p-1 text-secondary hover:text-danger opacity-0 group-hover:opacity-100"
                        onClick={() => void removeFavorite(f.id)}
                        aria-label={`删除收藏 ${f.name}`}
                        title="删除收藏"
                      >
                        <FiTrash2 size={11} />
                      </button>
                    </div>
                  ))}
                </div>
              </>
            )}
          </div>
          <button
            className="flex items-center justify-center w-6 h-6 text-secondary hover:text-ink rounded hover:bg-raised"
            onClick={() => void formatSQL()}
            aria-label="格式化 SQL (Ctrl+Shift+F)"
            title="格式化 SQL (Ctrl+Shift+F)"
          >
            <FiAlignLeft size={12} />
          </button>
          <button
            className="flex items-center justify-center w-6 h-6 text-secondary hover:text-ink rounded hover:bg-raised"
            onClick={() => adjustFont(-1)}
            aria-label="缩小字号"
            title="缩小字号"
          >
            <FiMinus size={12} />
          </button>
          <button
            className="flex items-center justify-center w-6 h-6 text-secondary hover:text-ink rounded hover:bg-raised"
            onClick={() => adjustFont(1)}
            aria-label="放大字号"
            title="放大字号"
          >
            <FiPlus size={12} />
          </button>
          <div className="relative">
            <button
              className={`flex items-center px-1 h-6 rounded hover:bg-raised ${
                settings.showColumnComments ? 'text-accent' : 'text-secondary hover:text-ink'
              }`}
              onClick={() => setShowSettings(v => !v)}
              aria-label="显示设置"
              aria-expanded={showSettings}
              title="显示设置"
            >
              <FiSettings size={13} />
              <FiChevronDown size={11} className="ml-1" />
            </button>

            {showSettings && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setShowSettings(false)} />
                <div className="absolute right-0 top-7 z-50 w-60 p-1.5 bg-panel border border-line-strong rounded shadow-xl">
                  <label className="flex items-start gap-2 p-1.5 text-xs text-ink hover:bg-raised rounded cursor-pointer">
                    <input
                      type="checkbox"
                      className="mt-0.5"
                      checked={settings.showColumnComments}
                      onChange={e =>
                        onSettingsChange({ ...settings, showColumnComments: e.target.checked })
                      }
                    />
                    <span>
                      结果显示字段注释
                      <span className="block text-2xs text-muted">在列头第二行以灰字显示</span>
                    </span>
                  </label>

                  <label className="block p-1.5 text-xs text-ink border-t border-line mt-1 pt-2">
                    <span>结果默认返回行数</span>
                    <span className="block text-2xs text-muted mb-1">
                      仅对 SELECT 生效，0 表示不限制
                    </span>
                    <input
                      type="number"
                      min={0}
                      step={100}
                      value={settings.resultLimit}
                      onChange={e =>
                        onSettingsChange({
                          ...settings,
                          resultLimit: Math.max(0, Number(e.target.value) || 0),
                        })
                      }
                      className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-xs text-ink focus:outline-none focus:border-accent"
                    />
                  </label>

                  <label className="block p-1.5 text-xs text-ink border-t border-line mt-1 pt-2">
                    <span>导出行数上限</span>
                    <span className="block text-2xs text-muted mb-1">
                      INSERT 脚本导出的上限，XLSX 为其十倍；0 使用默认值
                    </span>
                    <input
                      type="number"
                      min={0}
                      step={1000}
                      value={settings.exportMaxRows}
                      onChange={e =>
                        onSettingsChange({
                          ...settings,
                          exportMaxRows: Math.max(0, Number(e.target.value) || 0),
                        })
                      }
                      className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-xs text-ink focus:outline-none focus:border-accent"
                    />
                  </label>

                  <label className="block p-1.5 text-xs text-ink border-t border-line mt-1 pt-2">
                    <span>Safe Mode（写入保护）</span>
                    <span className="block text-2xs text-muted mb-1">
                      仅作用于 SQL 编辑器的写语句，不影响表浏览/网格编辑
                    </span>
                    <select
                      value={settings.safeMode}
                      onChange={e =>
                        onSettingsChange({
                          ...settings,
                          safeMode: e.target.value as typeof settings.safeMode,
                        })
                      }
                      className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-xs text-ink focus:outline-none focus:border-accent"
                    >
                      <option value="off">关闭</option>
                      <option value="confirmWrites">所有写操作都确认</option>
                      <option value="confirmNoWhere">无 WHERE 的写操作需确认</option>
                      <option value="blockNoWhere">禁止无 WHERE 的 UPDATE/DELETE</option>
                    </select>
                  </label>

                  <label className="block p-1.5 text-xs text-ink border-t border-line mt-1 pt-2">
                    <span>空闲自动锁定（分钟）</span>
                    <span className="block text-2xs text-muted mb-1">
                      0 表示不按空闲锁定；需先设置本地口令。锁定时会断开全部连接
                    </span>
                    <input
                      type="number"
                      min={0}
                      step={1}
                      value={settings.idleLockMinutes}
                      onChange={e =>
                        onSettingsChange({
                          ...settings,
                          idleLockMinutes: Math.max(0, Math.floor(Number(e.target.value) || 0)),
                        })
                      }
                      className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-xs text-ink focus:outline-none focus:border-accent"
                    />
                  </label>

                  <label className="flex items-start gap-2 p-1.5 text-xs text-ink hover:bg-raised rounded cursor-pointer border-t border-line mt-1 pt-2">
                    <input
                      type="checkbox"
                      className="mt-0.5"
                      checked={settings.lockOnHide}
                      onChange={e => onSettingsChange({ ...settings, lockOnHide: e.target.checked })}
                    />
                    <span>
                      窗口隐藏即锁定
                      <span className="block text-2xs text-muted">切到其他窗口或最小化时立刻锁定</span>
                    </span>
                  </label>

                  <label className="flex items-start gap-2 p-1.5 text-xs text-ink hover:bg-raised rounded cursor-pointer">
                    <input
                      type="checkbox"
                      className="mt-0.5"
                      checked={settings.lockOnSleep}
                      onChange={e => onSettingsChange({ ...settings, lockOnSleep: e.target.checked })}
                    />
                    <span>
                      休眠唤醒后锁定
                      <span className="block text-2xs text-muted">进程被冻结（挂起/合盖）即视为离开</span>
                    </span>
                  </label>

                  <div className="border-t border-line mt-1 pt-2 px-1.5">
                    {onManagePasscode && (
                      <button
                        className="w-full flex items-center gap-1.5 px-2 py-1 rounded text-2xs border border-line text-secondary hover:text-ink hover:bg-raised"
                        onClick={() => onManagePasscode()}
                      >
                        <FiLock size={11} aria-hidden />
                        {passcodeSet ? '修改或移除本地口令…' : '设置本地口令…'}
                      </button>
                    )}
                  </div>
                </div>
              </>
            )}
          </div>
        </div>
      </div>

      <div ref={hostRef} className="flex-1 min-h-0 overflow-hidden" />

      {paramDialog && (
        <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
          <div className="bg-panel rounded-lg w-full max-w-sm shadow-xl" role="dialog" aria-modal="true">
            <div className="flex items-center justify-between px-5 py-3 border-b border-line">
              <h2 className="text-sm font-semibold">填写查询参数</h2>
              <button className="text-secondary hover:text-ink" onClick={() => setParamDialog(null)} aria-label="取消">
                <FiX size={16} />
              </button>
            </div>
            <div className="p-5 space-y-3">
              <p className="text-2xs text-muted">语句里的 :参数 会替换成下面的值再执行；留空则保持原样。</p>
              {paramDialog.names.map((name, idx) => (
                <label key={name} className="block text-xs text-secondary">
                  <span className="mb-1 block font-mono">:{name}</span>
                  <input
                    type="text"
                    autoFocus={idx === 0}
                    value={paramDialog.values[name] ?? ''}
                    onChange={e =>
                      setParamDialog({
                        ...paramDialog,
                        values: { ...paramDialog.values, [name]: e.target.value },
                      })
                    }
                    onKeyDown={e => {
                      if (e.key === 'Enter') {
                        e.preventDefault();
                        confirmParams();
                      }
                    }}
                    className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink font-mono focus:outline-none focus:border-accent"
                  />
                </label>
              ))}
              <div className="flex justify-end gap-2 pt-2">
                <button
                  type="button"
                  className="px-3 py-1.5 rounded text-xs bg-raised hover:bg-emphasis text-ink"
                  onClick={() => setParamDialog(null)}
                >
                  取消
                </button>
                <button
                  type="button"
                  className="px-3 py-1.5 rounded text-xs bg-accent hover:bg-accent-hover text-on-accent flex items-center gap-1"
                  onClick={confirmParams}
                >
                  替换并执行
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
});

export default QueryEditor;
