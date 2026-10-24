import React, { forwardRef, useEffect, useImperativeHandle, useMemo, useRef } from 'react';
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
import { FiChevronDown, FiDatabase, FiLoader, FiMinus, FiPlay, FiPlus, FiSettings, FiSquare } from 'react-icons/fi';
import { AppSettings } from '../api';
import { ColumnInfo } from '../types';

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
    dialect,
    databases,
    database,
    onDatabaseChange,
    settings,
    onSettingsChange,
    getTables,
    getColumns,
  },
  ref,
) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const lastEmitted = useRef(query);
  const [showSettings, setShowSettings] = React.useState(false);

  // Callbacks change on every parent render; routing them through a ref lets
  // the editor instance be created once.
  const handlers = useRef({ onQueryChange, onExecute, getTables, getColumns, loading });
  handlers.current = { onQueryChange, onExecute, getTables, getColumns, loading };

  const fontCompartment = useMemo(() => new Compartment(), []);
  const dialectCompartment = useMemo(() => new Compartment(), []);
  const completionCompartment = useMemo(() => new Compartment(), []);
  const completerRef = useRef<ReturnType<typeof makeCompleter> | null>(null);

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
                if (!handlers.current.loading) handlers.current.onExecute();
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
          fontCompartment.of(fontSizeTheme(13)),
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

  const adjustFont = (delta: number) => {
    const view = viewRef.current;
    if (!view) return;
    const current = parseFloat(getComputedStyle(view.dom).fontSize) || 13;
    const next = Math.min(22, Math.max(10, current + delta));
    view.dispatch({ effects: fontCompartment.reconfigure(fontSizeTheme(next)) });
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
          onClick={onExecute}
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

        <span className="text-2xs text-muted whitespace-nowrap">
          {isConnected ? 'Ctrl+Enter 执行 · Ctrl+Space 补全关键字/表/列' : '尚未连接数据库'}
        </span>

        <div className="ml-auto flex items-center gap-1">
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
                </div>
              </>
            )}
          </div>
        </div>
      </div>

      <div ref={hostRef} className="flex-1 min-h-0 overflow-hidden" />
    </div>
  );
});

export default QueryEditor;
