import { useCallback, useEffect, useMemo, useState } from 'react';
import { FiAlertTriangle, FiArrowRight, FiCopy, FiPlay, FiRefreshCw } from 'react-icons/fi';

import { ApplySync, CompareSchemas, GetDatabases, GetSchemas } from '../api';
import type {
  ApplySyncResult,
  CompareEntry,
  CompareOptions,
  CompareResult,
  CompareTarget,
  Connection,
} from '../types/index.ts';

// What each kind of change is called on screen. The backend sends stable
// identifiers; the words are the UI's job.
const OP_LABELS: Record<string, string> = {
  create_table: '建表',
  drop_table: '删表',
  add_column: '新增列',
  drop_column: '删除列',
  alter_column: '修改列',
  create_index: '建索引',
  drop_index: '删索引',
  add_primary_key: '建主键',
  drop_primary_key: '删主键',
  add_foreign_key: '建外键',
  drop_foreign_key: '删外键',
};

const DEFAULT_OPTIONS: CompareOptions = {
  ignoreDefaults: false,
  ignoreNullability: false,
  ignoreIndexes: false,
  ignoreForeignKeys: false,
};

const emptyTarget = (connId = ''): CompareTarget => ({ connId, database: '', schema: '' });

export interface CompareViewProps {
  connections: Connection[];
  connectedIds: string[];
  // Prefilled when the view is opened from a node in the tree: the side the user
  // right-clicked becomes the source.
  initialSource?: CompareTarget;
  initialTarget?: CompareTarget;
}

/**
 * Schema comparison and synchronization.
 *
 * The flow is deliberately two steps with a human in between: compare produces a
 * plan of tickable changes, and only the ticked ones are executed. Destructive
 * changes start unticked, and the run button names how many of them are about to
 * happen before it does anything.
 */
export default function CompareView({ connections, connectedIds, initialSource, initialTarget }: CompareViewProps) {
  const live = useMemo(
    () => connections.filter(c => connectedIds.includes(c.id) && c.type !== 'redis'),
    [connections, connectedIds],
  );

  const [source, setSource] = useState<CompareTarget>(initialSource ?? emptyTarget(live[0]?.id ?? ''));
  const [target, setTarget] = useState<CompareTarget>(initialTarget ?? emptyTarget(live[0]?.id ?? ''));
  const [options, setOptions] = useState<CompareOptions>(DEFAULT_OPTIONS);

  const [result, setResult] = useState<CompareResult | null>(null);
  const [selected, setSelected] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(false);
  const [applying, setApplying] = useState(false);
  const [applyResult, setApplyResult] = useState<ApplySyncResult | null>(null);
  const [error, setError] = useState('');
  const [showScript, setShowScript] = useState(false);

  const [databases, setDatabases] = useState<Record<string, string[]>>({});
  const [schemas, setSchemas] = useState<Record<string, string[]>>({});

  const loadDatabases = useCallback(async (connId: string) => {
    if (!connId || databases[connId]) return;
    try {
      const found = await GetDatabases(connId);
      setDatabases(current => ({ ...current, [connId]: found }));
    } catch {
      setDatabases(current => ({ ...current, [connId]: [] }));
    }
  }, [databases]);

  const loadSchemas = useCallback(async (connId: string, database: string) => {
    const key = `${connId}/${database}`;
    if (!connId || schemas[key]) return;
    try {
      const found = await GetSchemas(connId, database);
      setSchemas(current => ({ ...current, [key]: found }));
    } catch {
      setSchemas(current => ({ ...current, [key]: [] }));
    }
  }, [schemas]);

  useEffect(() => {
    void loadDatabases(source.connId);
    void loadDatabases(target.connId);
  }, [source.connId, target.connId, loadDatabases]);

  useEffect(() => {
    void loadSchemas(source.connId, source.database);
    void loadSchemas(target.connId, target.database);
  }, [source.connId, source.database, target.connId, target.database, loadSchemas]);

  // A connection the user just picked has no database yet; take the one it was
  // opened with rather than leaving the side empty.
  const pickConnection = (side: 'source' | 'target', connId: string) => {
    const conn = live.find(c => c.id === connId);
    const next = emptyTarget(connId);
    if (conn) next.database = conn.database ?? '';
    if (side === 'source') setSource(next);
    else setTarget(next);
  };

  const runCompare = async () => {
    setLoading(true);
    setError('');
    setApplyResult(null);
    try {
      const plan = await CompareSchemas(source, target, options);
      setResult(plan);
      setShowScript(false);
      // Start from the backend's own advice on what to tick, so a destructive
      // change is never taken for granted.
      const initial: Record<string, boolean> = {};
      for (const entry of plan.entries) initial[entry.id] = entry.selected;
      setSelected(initial);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setResult(null);
    } finally {
      setLoading(false);
    }
  };

  const runApply = async () => {
    if (!result) return;
    const ids = result.entries.filter(e => selected[e.id]).map(e => e.id);
    const destructive = result.entries.filter(e => selected[e.id] && e.destructive).length;
    if (destructive > 0) {
      const ok = window.confirm(
        `将执行 ${ids.length} 条语句，其中 ${destructive} 条是破坏性变更（删表/删列/修改列），可能造成数据丢失且无法自动回滚。确定继续？`,
      );
      if (!ok) return;
    }
    setApplying(true);
    setError('');
    try {
      setApplyResult(await ApplySync(result.planId, ids));
      // Re-read both sides: the plan is spent, and the user needs to see what is
      // left rather than a stale list.
      await runCompare();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setApplying(false);
    }
  };

  const visibleStatements = useMemo(() => {
    if (!result) return [];
    const ids = new Set(result.entries.filter(e => selected[e.id]).map(e => e.id));
    return result.statements.filter(s => ids.has(s.entryId));
  }, [result, selected]);

  const selectedCount = result ? result.entries.filter(e => selected[e.id]).length : 0;
  const destructiveCount = result ? result.entries.filter(e => selected[e.id] && e.destructive).length : 0;

  return (
    <div className="flex flex-col h-full min-h-0 text-ink">
      <div className="flex flex-wrap items-end gap-3 px-3 py-2 border-b border-edge">
        <TargetPicker
          label="源（以它为准）"
          target={source}
          connections={live}
          databases={databases[source.connId] ?? []}
          schemas={schemas[`${source.connId}/${source.database}`] ?? []}
          onChange={setSource}
          onPickConnection={id => pickConnection('source', id)}
        />
        <FiArrowRight size={14} aria-hidden className="mb-2 text-secondary" />
        <TargetPicker
          label="目标（要被改成源的样子）"
          target={target}
          connections={live}
          databases={databases[target.connId] ?? []}
          schemas={schemas[`${target.connId}/${target.database}`] ?? []}
          onChange={setTarget}
          onPickConnection={id => pickConnection('target', id)}
        />
        <button
          className="px-2.5 py-1.5 text-xs bg-accent text-white hover:opacity-90 disabled:opacity-40"
          onClick={() => void runCompare()}
          disabled={loading || !source.connId || !target.connId}
        >
          {loading ? '对比中…' : '对比'}
        </button>
        <label className="flex items-center gap-1 text-2xs text-secondary">
          <input
            type="checkbox"
            checked={options.ignoreIndexes}
            onChange={e => setOptions({ ...options, ignoreIndexes: e.target.checked })}
          />
          忽略索引
        </label>
        <label className="flex items-center gap-1 text-2xs text-secondary">
          <input
            type="checkbox"
            checked={options.ignoreForeignKeys}
            onChange={e => setOptions({ ...options, ignoreForeignKeys: e.target.checked })}
          />
          忽略外键
        </label>
        <label className="flex items-center gap-1 text-2xs text-secondary">
          <input
            type="checkbox"
            checked={options.ignoreDefaults}
            onChange={e => setOptions({ ...options, ignoreDefaults: e.target.checked })}
          />
          忽略默认值
        </label>
        <label className="flex items-center gap-1 text-2xs text-secondary">
          <input
            type="checkbox"
            checked={options.ignoreNullability}
            onChange={e => setOptions({ ...options, ignoreNullability: e.target.checked })}
          />
          忽略空值约束
        </label>
      </div>

      {error && (
        <div className="px-3 py-1.5 text-xs text-danger border-b border-edge">{error}</div>
      )}

      {result && (
        <div className="flex items-center gap-2 px-3 py-1.5 text-2xs text-secondary border-b border-edge">
          <span>
            {result.sourceLabel} <span className="text-muted">→</span> {result.targetLabel}
          </span>
          <span className="text-muted">·</span>
          <span>{result.entries.length} 项差异</span>
          {result.entries.length > 0 && (
            <>
              <button className="hover:text-ink" onClick={() => setSelected(tick(result.entries, true))}>
                全选
              </button>
              <button className="hover:text-ink" onClick={() => setSelected(tick(result.entries, false))}>
                全不选
              </button>
              <button
                className="hover:text-ink"
                onClick={() => setSelected(tick(result.entries, false, e => !e.destructive))}
              >
                只选非破坏性
              </button>
            </>
          )}
          <span className="ml-auto flex items-center gap-2">
            <button className="flex items-center gap-1 hover:text-ink" onClick={() => setShowScript(v => !v)}>
              <FiCopy size={11} aria-hidden />
              脚本（{visibleStatements.length} 条）
            </button>
            <button className="flex items-center gap-1 hover:text-ink" onClick={() => void runCompare()}>
              <FiRefreshCw size={11} aria-hidden />
              重新对比
            </button>
          </span>
        </div>
      )}

      {result?.warnings && result.warnings.length > 0 && (
        <div className="px-3 py-1.5 text-2xs text-warning border-b border-edge">
          {result.warnings.map(w => (
            <div key={w} className="flex items-start gap-1.5">
              <FiAlertTriangle size={11} aria-hidden className="mt-0.5 shrink-0" />
              <span>{w}</span>
            </div>
          ))}
        </div>
      )}

      {applyResult && (
        <div className="px-3 py-1.5 text-2xs border-b border-edge">
          {applyResult.failures.length === 0 ? (
            <span className="text-success">
              已执行 {applyResult.applied} / {applyResult.total} 条语句
            </span>
          ) : (
            <div className="text-danger">
              <div>
                执行到第 {applyResult.applied} 条时停止（共 {applyResult.total} 条）：
                {applyResult.failures[0].error}
              </div>
              <code className="block mt-1 text-2xs text-muted break-all">{applyResult.failures[0].sql}</code>
            </div>
          )}
        </div>
      )}

      <div className="flex-1 min-h-0 overflow-auto">
        {!result ? (
          <div className="p-6 text-xs text-muted">
            选择两侧连接后点「对比」。对比只读取目录元数据，不会改动任何数据。
          </div>
        ) : result.entries.length === 0 ? (
          <div className="p-6 text-xs text-muted">两侧结构一致，没有需要同步的差异。</div>
        ) : (
          <table className="w-full text-xs">
            <thead className="sticky top-0 bg-canvas text-2xs text-muted">
              <tr>
                <th className="w-8" />
                <th className="text-left font-normal px-2 py-1">变更</th>
                <th className="text-left font-normal px-2 py-1">对象</th>
                <th className="text-left font-normal px-2 py-1">从</th>
                <th className="text-left font-normal px-2 py-1">到</th>
                <th className="text-left font-normal px-2 py-1">说明</th>
              </tr>
            </thead>
            <tbody>
              {result.entries.map(entry => (
                <tr key={entry.id} className="border-t border-edge/60">
                  <td className="px-2 py-1 align-top">
                    <input
                      type="checkbox"
                      checked={!!selected[entry.id]}
                      onChange={e => setSelected({ ...selected, [entry.id]: e.target.checked })}
                      aria-label={`选择 ${entry.table}${entry.object ? '.' + entry.object : ''}`}
                    />
                  </td>
                  <td className="px-2 py-1 align-top whitespace-nowrap">
                    <span className={entry.destructive ? 'text-danger' : 'text-accent'}>
                      {OP_LABELS[entry.op] ?? entry.op}
                    </span>
                  </td>
                  <td className="px-2 py-1 align-top whitespace-nowrap">
                    {entry.table}
                    {entry.object && <span className="text-muted">.{entry.object}</span>}
                  </td>
                  <td className="px-2 py-1 align-top text-muted break-all">{entry.from || '—'}</td>
                  <td className="px-2 py-1 align-top break-all">{entry.to || '—'}</td>
                  <td className="px-2 py-1 align-top text-muted">{entry.reason}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        {showScript && visibleStatements.length > 0 && (
          <pre className="m-3 p-2 text-2xs bg-raised border border-edge overflow-auto whitespace-pre-wrap">
            {visibleStatements.map(s => s.sql).join('\n')}
          </pre>
        )}
      </div>

      {result && result.entries.length > 0 && (
        <div className="flex items-center gap-3 px-3 py-2 border-t border-edge">
          <button
            className="flex items-center gap-1.5 px-2.5 py-1.5 text-xs bg-accent text-white hover:opacity-90 disabled:opacity-40"
            onClick={() => void runApply()}
            disabled={applying || selectedCount === 0}
          >
            <FiPlay size={11} aria-hidden />
            {applying ? '执行中…' : `执行 ${selectedCount} 项`}
          </button>
          {destructiveCount > 0 && (
            <span className="flex items-center gap-1 text-2xs text-danger">
              <FiAlertTriangle size={11} aria-hidden />
              含 {destructiveCount} 项破坏性变更，执行前会再次确认；同步不是事务，失败的语句不会自动回滚
            </span>
          )}
          <span className="ml-auto text-2xs text-muted">脚本按依赖顺序执行：先删约束，再改表，最后建索引与外键</span>
        </div>
      )}
    </div>
  );
}

// tick builds a selection map in one pass, so the three bulk buttons share the
// rule instead of each re-deriving it.
function tick(entries: CompareEntry[], value: boolean, keep?: (e: CompareEntry) => boolean): Record<string, boolean> {
  const out: Record<string, boolean> = {};
  for (const entry of entries) out[entry.id] = keep ? keep(entry) : value;
  return out;
}

interface TargetPickerProps {
  label: string;
  target: CompareTarget;
  connections: Connection[];
  databases: string[];
  schemas: string[];
  onChange: (next: CompareTarget) => void;
  onPickConnection: (connId: string) => void;
}

function TargetPicker({ label, target, connections, databases, schemas, onChange, onPickConnection }: TargetPickerProps) {
  const selectClass = 'text-xs bg-raised border border-edge px-1.5 py-1 text-ink';
  return (
    <div className="flex flex-col gap-1">
      <span className="text-2xs text-muted">{label}</span>
      <div className="flex items-center gap-1">
        <select
          className={selectClass}
          value={target.connId}
          onChange={e => onPickConnection(e.target.value)}
          aria-label={`${label} 连接`}
        >
          <option value="">选择连接…</option>
          {connections.map(c => (
            <option key={c.id} value={c.id}>
              {c.name || c.id}
            </option>
          ))}
        </select>
        {databases.length > 0 && (
          <select
            className={selectClass}
            value={target.database}
            onChange={e => onChange({ ...target, database: e.target.value, schema: '' })}
            aria-label={`${label} 数据库`}
          >
            <option value="">默认库</option>
            {databases.map(d => (
              <option key={d} value={d}>
                {d}
              </option>
            ))}
          </select>
        )}
        {schemas.length > 0 && (
          <select
            className={selectClass}
            value={target.schema}
            onChange={e => onChange({ ...target, schema: e.target.value })}
            aria-label={`${label} schema`}
          >
            <option value="">默认 schema</option>
            {schemas.map(s => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        )}
      </div>
    </div>
  );
}
