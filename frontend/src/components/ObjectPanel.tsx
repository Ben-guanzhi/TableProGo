import React, { useState } from 'react';
import {
  FiAlertCircle,
  FiCheck,
  FiChevronLeft,
  FiChevronRight,
  FiCopy,
  FiEye,
  FiKey,
  FiLoader,
  FiRefreshCw,
  FiTable,
  FiTrash2,
  FiX,
} from 'react-icons/fi';
import {
  HistoryEntry,
  ObjectSection,
  QueryResult,
  SectionState,
  SqlFilter,
  TableStructure,
  TableRef,
} from '../types';
import toast from 'react-hot-toast';
import { GetTriggerDefinition } from '../api';
import { copyText } from '../clipboard';
import DataGrid from './DataGrid';
import ErrorBanner from './ErrorBanner';
import FilterBar from './FilterBar';

const sectionLabels: Record<ObjectSection, string> = {
  data: '数据',
  structure: '结构',
  query: '查询',
  history: '历史',
};

export const DEFAULT_PAGE_SIZE = 100;
const PAGE_SIZES = [20, 50, 100, 200, 500];

interface ObjectPanelProps {
  tableName: string;
  qualifier: string;
  objectType: 'table' | 'view';
  section: ObjectSection;
  onSectionChange: (section: ObjectSection) => void;
  data: SectionState<QueryResult>;
  // The data card's own page; rows arrive one page at a time.
  dataPage?: number;
  dataPageSize?: number;
  onDataPage?: (page: number) => void;
  onDataPageSize?: (pageSize: number) => void;
  // Conditions the data card filters by on the server. Without a handler the
  // bar is hidden, because nothing could re-read the page.
  dataFilters?: SqlFilter[];
  onDataFilters?: (filters: SqlFilter[]) => void;
  structure: SectionState<TableStructure>;
  history: SectionState<HistoryEntry[]>;
  // The query page owns its editor, so the panel only hosts it.
  queryPage?: React.ReactNode;
  comments?: Record<string, string>;
  showComments?: boolean;
  // The data card's rows can be written back when they came from a table.
  editSource?: TableRef | null;
  exportName: string;
  // The object this panel belongs to, for features that re-read the catalog
  // (the trigger definition lookup).
  tableRef?: TableRef | null;
  // Optional: opens the structure editor for this table.
  onEditStructure?: () => void;
  // Optional: opens the import dialog for this table.
  onImport?: () => void;
  onRefresh: () => void;
  onDataApplied?: () => void;
  onPendingChange?: (total: number) => void;
  onDismissError: (section: ObjectSection) => void;
  onUseStatement?: (sql: string) => void;
  onClearHistory?: () => void;
}

const Card: React.FC<{ title: string; count?: number; actions?: React.ReactNode; children: React.ReactNode }> = ({
  title,
  count,
  actions,
  children,
}) => (
  <section className="bg-panel border border-line rounded shrink-0">
    <header className="flex items-center px-3 h-8 border-b border-line-soft">
      <h3 className="text-xs font-semibold text-secondary uppercase tracking-wide">{title}</h3>
      {count !== undefined && (
        <span className="ml-1.5 px-1.5 rounded-full bg-raised text-2xs text-secondary">{count}</span>
      )}
      {actions && <div className="ml-auto flex items-center gap-1">{actions}</div>}
    </header>
    {children}
  </section>
);

const gridClass = 'w-full text-[0.78125rem] border-collapse';
const thClass =
  'text-left font-medium text-secondary bg-panel px-2.5 py-1.5 border-b border-line whitespace-nowrap';
const tdClass = 'px-2.5 py-1.5 border-b border-line-soft align-top';
const monoClass = 'font-mono';

const ColumnList: React.FC<{ structure: TableStructure; showComments: boolean }> = ({
  structure,
  showComments,
}) => (
  <table className={gridClass}>
    <thead>
      <tr>
        <th className={thClass}>字段</th>
        <th className={thClass}>类型</th>
        <th className={thClass}>空值</th>
        <th className={thClass}>默认值</th>
        {showComments && <th className={thClass}>注释</th>}
      </tr>
    </thead>
    <tbody>
      {structure.columns.map(c => (
        <tr key={c.name} className="even:bg-row-alt/40">
          <td className={`${tdClass} ${monoClass}`}>
            {c.isPrimaryKey && <FiKey size={11} className="mr-1 inline-block text-warning align-[-1px]" />}
            {c.name}
          </td>
          <td className={`${tdClass} ${monoClass} text-secondary`}>{c.type}</td>
          <td className={tdClass}>{c.nullable ? 'YES' : 'NO'}</td>
          <td className={`${tdClass} ${monoClass} text-muted`}>{c.defaultValue || ''}</td>
          {showComments && <td className={`${tdClass} text-muted`}>{c.comment}</td>}
        </tr>
      ))}
    </tbody>
  </table>
);

const IndexList: React.FC<{ structure: TableStructure }> = ({ structure }) => (
  <table className={gridClass}>
    <thead>
      <tr>
        <th className={thClass}>名称</th>
        <th className={thClass}>类型</th>
        <th className={thClass}>方法</th>
        <th className={thClass}>唯一</th>
        <th className={thClass}>字段</th>
      </tr>
    </thead>
    <tbody>
      {structure.indexes.map(i => (
        <tr key={i.name} className="even:bg-row-alt/40">
          <td className={`${tdClass} ${monoClass}`}>{i.name}</td>
          <td className={`${tdClass} text-secondary`}>
            {i.primary ? 'PRIMARY' : i.kind || 'INDEX'}
          </td>
          <td className={`${tdClass} text-secondary`}>{i.method || ''}</td>
          <td className={tdClass}>{i.unique ? 'YES' : 'NO'}</td>
          <td className={`${tdClass} ${monoClass}`}>
            {(i.parts?.length ? i.parts : i.columns).join(', ')}
            {i.predicate ? (
              <div className="text-muted text-2xs font-sans">WHERE {i.predicate}</div>
            ) : null}
          </td>
        </tr>
      ))}
    </tbody>
  </table>
);

const ForeignKeyList: React.FC<{ structure: TableStructure }> = ({ structure }) => (
  <table className={gridClass}>
    <thead>
      <tr>
        <th className={thClass}>名称</th>
        <th className={thClass}>字段</th>
        <th className={thClass}>引用</th>
        <th className={thClass}>ON DELETE</th>
        <th className={thClass}>ON UPDATE</th>
      </tr>
    </thead>
    <tbody>
      {structure.foreignKeys.map(fk => (
        <tr key={fk.name} className="even:bg-row-alt/40">
          <td className={`${tdClass} ${monoClass}`}>{fk.name}</td>
          <td className={`${tdClass} ${monoClass}`}>{fk.columns.join(', ')}</td>
          <td className={`${tdClass} ${monoClass}`}>
            {fk.refSchema ? `${fk.refSchema}.` : ''}
            {fk.refTable}
            <span className="text-muted">({fk.refColumns.join(', ')})</span>
          </td>
          <td className={`${tdClass} text-secondary`}>{fk.onDelete}</td>
          <td className={`${tdClass} text-secondary`}>{fk.onUpdate}</td>
        </tr>
      ))}
    </tbody>
  </table>
);

// CHECK constraints come from the kernel's ExtraMetadataReader; SQLite has no
// constraint catalog, so the card simply does not appear there.
const CheckList: React.FC<{ structure: TableStructure }> = ({ structure }) => (
  <table className={gridClass}>
    <thead>
      <tr>
        <th className={thClass}>名称</th>
        <th className={thClass}>表达式</th>
      </tr>
    </thead>
    <tbody>
      {structure.checks?.map(c => (
        <tr key={c.name} className="even:bg-row-alt/40">
          <td className={`${tdClass} ${monoClass}`}>{c.name}</td>
          <td className={`${tdClass} ${monoClass} text-secondary`}>{c.expression}</td>
        </tr>
      ))}
    </tbody>
  </table>
);

const TriggerList: React.FC<{
  structure: TableStructure;
  connId: string;
  database: string;
  schema: string;
}> = ({ structure, connId, database, schema }) => {
  const [openTrigger, setOpenTrigger] = useState<{ name: string; def: string } | null>(null);
  return (
    <>
      <table className={gridClass}>
        <thead>
          <tr>
            <th className={thClass}>名称</th>
            <th className={thClass}>时机</th>
            <th className={thClass}>事件</th>
            <th className={thClass}>状态</th>
            <th className={thClass}>定义</th>
          </tr>
        </thead>
        <tbody>
          {structure.triggers?.map(tr => (
            <tr key={tr.name} className="even:bg-row-alt/40">
              <td className={`${tdClass} ${monoClass}`}>{tr.name}</td>
              <td className={tdClass}>{tr.timing}</td>
              <td className={`${tdClass} text-secondary`}>{tr.event}</td>
              <td className={tdClass}>{tr.enabled}</td>
              <td className={tdClass}>
                <button
                  className="text-accent hover:underline text-2xs"
                  onClick={() => {
                    GetTriggerDefinition(connId, database, schema, tr.name)
                      .then(def => setOpenTrigger({ name: tr.name, def }))
                      .catch(e => toast.error((e as Error).message));
                  }}
                >
                  查看
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {openTrigger && (
        <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50" onClick={() => setOpenTrigger(null)}>
          <div
            className="bg-panel rounded-lg w-full max-w-2xl max-h-[70vh] shadow-xl flex flex-col"
            onClick={e => e.stopPropagation()}
            role="dialog"
            aria-modal="true"
          >
            <div className="flex items-center justify-between px-5 py-3 border-b border-line">
              <h2 className="text-sm font-semibold font-mono">{openTrigger.name}</h2>
              <button className="text-secondary hover:text-ink" onClick={() => setOpenTrigger(null)} aria-label="关闭">
                <FiX size={16} />
              </button>
            </div>
            <pre className="flex-1 overflow-auto p-4 text-xs font-mono text-ink whitespace-pre-wrap break-all">
              {openTrigger.def}
            </pre>
          </div>
        </div>
      )}
    </>
  );
};

const DdlCard: React.FC<{ ddl: string; label: string }> = ({ ddl, label }) => {
  const [expanded, setExpanded] = useState(false);
  const [copied, setCopied] = useState(false);

  const copy = () => {
    void copyText(ddl).then(ok => {
      if (ok) {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      } else {
        toast.error('复制失败');
      }
    });
  };

  const long = ddl.split('\n').length > 12;
  return (
    <Card
      title="建表语句"
      actions={
        <>
          {long && (
            <button
              className="px-1.5 py-0.5 text-2xs text-secondary hover:text-ink"
              onClick={() => setExpanded(v => !v)}
            >
              {expanded ? '收起' : '展开'}
            </button>
          )}
          <button
            className="px-1.5 py-0.5 flex items-center gap-1 text-2xs text-secondary hover:text-ink"
            onClick={copy}
            title={label}
          >
            {copied ? <FiCheck size={11} /> : <FiCopy size={11} />}
            {copied ? '已复制' : '复制'}
          </button>
        </>
      }
    >
      <pre
        className={`px-3 py-2 ${monoClass} text-[0.75rem] leading-relaxed text-ink overflow-auto whitespace-pre ${
          expanded ? 'max-h-[45vh]' : long ? 'max-h-32' : ''
        }`}
      >
        {ddl}
      </pre>
    </Card>
  );
};

const Loading: React.FC<{ label: string }> = ({ label }) => (
  <div className="flex-1 flex flex-col items-center justify-center gap-2 text-muted text-sm">
    <FiLoader className="animate-spin" size={18} />
    <span>{label}</span>
  </div>
);

const historyTime = (ms: number) => {
  const date = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(
    date.getMinutes(),
  )}:${pad(date.getSeconds())}`;
};

const HistoryList: React.FC<{
  entries: HistoryEntry[];
  onUse?: (sql: string) => void;
}> = ({ entries, onUse }) => (
  <div className="flex flex-col gap-2">
    {entries.map(entry => (
      <article
        key={entry.id}
        className="bg-panel border border-line rounded shrink-0"
        onClick={onUse ? () => onUse(entry.sql) : undefined}
        title={onUse ? '点击移到查询页' : undefined}
      >
        <header className="flex items-center gap-2 px-2.5 h-7 border-b border-line-soft text-2xs text-muted">
          <span className={entry.error ? 'text-danger' : 'text-success'}>
            {entry.error ? '失败' : '成功'}
          </span>
          <span className={`${monoClass} tabular-nums`}>{historyTime(entry.time)}</span>
          {entry.database && <span className="truncate">{entry.database}</span>}
          <span>{entry.rowCount} 行</span>
          {entry.affectedRows > 0 && <span>影响 {entry.affectedRows} 行</span>}
          <span className={`${monoClass} tabular-nums`}>{entry.executionTime.toFixed(3)}s</span>
        </header>
        <pre className="px-3 py-2 font-mono text-[0.75rem] leading-relaxed text-ink whitespace-pre-wrap break-all max-h-40 overflow-auto">
          {entry.sql}
        </pre>
        {entry.error && (
          <div className="px-3 py-1.5 border-t border-line-soft text-[0.75rem] text-danger whitespace-pre-wrap break-all">
            {entry.error}
          </div>
        )}
      </article>
    ))}
  </div>
);


// A full page means there is very likely another one behind it; a short page is
// the last one. Counting rows up front would cost a second query per page.
const DataPagination: React.FC<{
  page: number;
  pageSize: number;
  rowCount: number;
  onPage: (page: number) => void;
  onPageSize: (pageSize: number) => void;
}> = ({ page, pageSize, rowCount, onPage, onPageSize }) => {
  const navClass =
    'flex items-center justify-center w-6 h-6 rounded text-secondary hover:text-ink hover:bg-raised disabled:opacity-35 disabled:cursor-not-allowed';
  return (
    <div className="flex items-center gap-2 px-3 py-1 bg-panel border-b border-line text-2xs text-muted shrink-0">
      <span className="tabular-nums">
        第 {page} 页 · 本页 {rowCount} 行
      </span>
      <div className="ml-auto flex items-center gap-2">
        <label className="flex items-center gap-1">
          每页
          <select
            className="bg-canvas border border-line rounded px-1 py-0.5 text-2xs text-ink focus:outline-none focus:border-accent"
            value={pageSize}
            onChange={e => onPageSize(Number(e.target.value))}
            aria-label="每页行数"
            title="每页读取多少行"
          >
            {PAGE_SIZES.map(n => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
        <button className={navClass} onClick={() => onPage(page - 1)} disabled={page <= 1} aria-label="上一页" title="上一页">
          <FiChevronLeft size={13} />
        </button>
        <button
          className={navClass}
          onClick={() => onPage(page + 1)}
          disabled={rowCount < pageSize}
          aria-label="下一页"
          title="下一页"
        >
          <FiChevronRight size={13} />
        </button>
      </div>
    </div>
  );
};

const ObjectPanel: React.FC<ObjectPanelProps> = ({
  tableName,
  qualifier,
  objectType,
  section,
  onSectionChange,
  data,
  dataPage,
  dataPageSize,
  dataFilters,
  onDataFilters,
  onDataPage,
  onDataPageSize,
  structure,
  history,
  queryPage,
  comments,
  showComments,
  editSource,
  exportName,
  tableRef,
  onEditStructure,
  onImport,
  onRefresh,
  onDataApplied,
  onPendingChange,
  onDismissError,
  onUseStatement,
  onClearHistory,
}) => {
  const full = qualifier ? `${qualifier}.${tableName}` : tableName;

  return (
    <div className="flex-1 flex flex-col min-h-0 bg-canvas">
      <div className="flex items-center px-3 h-10 bg-panel border-b border-line shrink-0">
        {objectType === 'view'
          ? <FiEye size={13} aria-hidden className="mr-2 shrink-0 text-icon-view" />
          : <FiTable size={13} aria-hidden className="mr-2 shrink-0 text-success" />}
        <span className={`${monoClass} text-sm truncate`} title={full}>
          {tableName}
        </span>
        {qualifier && (
          <span className={`ml-2 ${monoClass} text-2xs text-muted truncate`}>{qualifier}</span>
        )}

        <div className="mx-auto flex items-center p-0.5 bg-raised border border-line rounded">
          {(Object.keys(sectionLabels) as ObjectSection[]).map(key => (
            <button
              key={key}
              aria-pressed={section === key}
              className={`px-3 py-0.5 text-xs rounded ${
                section === key ? 'bg-panel text-ink shadow-sm' : 'text-secondary hover:text-ink'
              }`}
              onClick={() => onSectionChange(key)}
            >
              {sectionLabels[key]}
            </button>
          ))}
        </div>

        {section !== 'query' ? (
          <button
            className="flex items-center justify-center w-6 h-6 ml-auto text-secondary hover:text-ink shrink-0"
            onClick={onRefresh}
            aria-label="重新加载当前分页"
            title="重新加载当前分页"
          >
            <FiRefreshCw size={13} />
          </button>
        ) : (
          <span className="ml-auto w-6 shrink-0" />
        )}
      </div>

      {section === 'query' && (
        <div className="flex-1 flex flex-col min-h-0">{queryPage}</div>
      )}

      {section === 'history' && (
        <>
          {history.loading && <Loading label="读取历史中…" />}
          {!history.loading && history.error && (
            <ErrorBanner message={history.error} onDismiss={() => onDismissError('history')} />
          )}
          {!history.loading && !history.error && history.data && (
            <div className="flex-1 min-h-0 overflow-y-auto p-3 flex flex-col gap-2">
              <div className="flex items-center gap-2 text-2xs text-muted shrink-0">
                <span>共 {history.data.length} 条</span>
                <span className="truncate">只记录本应用执行过的语句，点击语句可移到查询页</span>
                {history.data.length > 0 && onClearHistory && (
                  <button
                    className="ml-auto flex items-center gap-1 px-1.5 py-0.5 rounded text-2xs text-secondary hover:text-danger hover:bg-raised shrink-0"
                    onClick={() => {
                      if (window.confirm('确定清空该表相关的全部执行历史吗？此操作不可恢复。')) onClearHistory();
                    }}
                    title="清空该表相关的执行历史"
                  >
                    <FiTrash2 size={11} />
                    清空
                  </button>
                )}
              </div>
              {history.data.length > 0 ? (
                <HistoryList entries={history.data} onUse={onUseStatement} />
              ) : (
                <div className="py-10 text-center text-muted text-sm">
                  该表还没有执行过的语句，在查询页运行一条试试
                </div>
              )}
            </div>
          )}
          {!history.loading && !history.error && !history.data && (
            <div className="flex-1 flex items-center justify-center text-muted text-sm">
              <span>尚未读取历史，点击刷新加载</span>
            </div>
          )}
        </>
      )}

      {section === 'data' && (
        <>
          {onDataFilters && (data.data?.columns?.length ?? 0) > 0 && (
            <FilterBar
              columns={(data.data?.columns ?? []).map(col => ({ name: col.name, type: col.type }))}
              filters={dataFilters ?? []}
              busy={data.loading}
              onChange={onDataFilters}
            />
          )}
          {data.loading && <Loading label="加载数据中…" />}
          {!data.loading && data.error && (
            <ErrorBanner message={data.error} onDismiss={() => onDismissError('data')} />
          )}
          {!data.loading && data.data && (
            <div className="flex-1 flex flex-col min-h-0">
              {onDataPage && onDataPageSize && (
                <DataPagination
                  page={dataPage ?? 1}
                  pageSize={dataPageSize ?? DEFAULT_PAGE_SIZE}
                  rowCount={data.data.rowCount}
                  onPage={onDataPage}
                  onPageSize={onDataPageSize}
                />
              )}
              <DataGrid
                result={data.data}
                comments={comments}
                showComments={showComments}
                editSource={editSource}
                onApplied={onDataApplied}
                onPendingChange={onPendingChange}
                onDismissError={() => onDismissError('data')}
                exportName={exportName}
                onImport={onImport}
              />
            </div>
          )}
          {!data.loading && !data.error && !data.data && (
            <div className="flex-1 flex items-center justify-center text-muted text-sm">
              <span>没有数据，点击刷新重新加载</span>
            </div>
          )}
        </>
      )}

      {section === 'structure' && (
        <>
          {structure.loading && <Loading label="读取结构中…" />}
          {!structure.loading && structure.error && (
            <ErrorBanner message={structure.error} onDismiss={() => onDismissError('structure')} />
          )}
          {!structure.loading && structure.data && (
            <div className="flex-1 min-h-0 overflow-y-auto p-3 flex flex-col gap-3">
              <Card
                title="字段"
                count={structure.data.columns.length}
                actions={
                  onEditStructure && objectType === 'table' ? (
                    <button
                      className="px-1.5 py-0.5 text-2xs text-secondary hover:text-ink"
                      onClick={onEditStructure}
                    >
                      编辑结构…
                    </button>
                  ) : undefined
                }
              >
                <ColumnList structure={structure.data} showComments={!!showComments} />
              </Card>
              {structure.data.indexes.length > 0 && (
                <Card title="索引" count={structure.data.indexes.length}>
                  <IndexList structure={structure.data} />
                </Card>
              )}
              {structure.data.foreignKeys.length > 0 && (
                <Card title="外键" count={structure.data.foreignKeys.length}>
                  <ForeignKeyList structure={structure.data} />
                </Card>
              )}
              {(structure.data.triggers?.length ?? 0) > 0 && tableRef && (
                <Card title="触发器" count={structure.data.triggers!.length}>
                  <TriggerList
                    structure={structure.data}
                    connId={tableRef.connId}
                    database={tableRef.database}
                    schema={tableRef.schema}
                  />
                </Card>
              )}
              {(structure.data.checks?.length ?? 0) > 0 && (
                <Card title="CHECK 约束" count={structure.data.checks!.length}>
                  <CheckList structure={structure.data} />
                </Card>
              )}
              {structure.data.viewDefinition && (
                <DdlCard ddl={structure.data.viewDefinition} label={`复制 ${full} 的定义`} />
              )}
              {structure.data.createTable && (
                <DdlCard ddl={structure.data.createTable} label={`复制 ${full} 的建表语句`} />
              )}
              {structure.data.columns.length === 0 && !structure.data.createTable && (
                <div className="flex items-center gap-1.5 text-muted text-sm p-4">
                  <FiAlertCircle size={13} />
                  <span>未读取到该对象的字段</span>
                </div>
              )}
            </div>
          )}
          {!structure.loading && !structure.error && !structure.data && (
            <div className="flex-1 flex items-center justify-center text-muted text-sm">
              <span>尚未读取结构，点击刷新加载</span>
            </div>
          )}
        </>
      )}
    </div>
  );
};

export default ObjectPanel;
