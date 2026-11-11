import React, { useEffect, useRef, useState } from 'react';
import { FiAlertTriangle, FiCheck, FiLoader, FiPlus, FiRefreshCw, FiTrash2, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  ApplyRowSync,
  PreviewRowSync,
  RowSyncApplyResult,
  RowSyncChange,
  RowSyncPreview,
  RowSyncRequest,
} from '../api';
import type { Connection } from '../types';
import { useDialog } from '../dialog';

interface RowSyncDialogProps {
  connections: Connection[];
  connectedIds: string[];
  // The connection the navigator was on, used as the initial source.
  initialSource?: { connId: string; database: string; schema: string };
  confirm: (request: {
    title: string;
    message: string;
    confirmLabel: string;
    destructive?: boolean;
  }) => Promise<boolean>;
  onClose: () => void;
}

const opLabel: Record<RowSyncChange['op'], string> = {
  insert: '新增',
  update: '修改',
  delete: '删除',
};

const opIcon = (op: RowSyncChange['op']) => {
  switch (op) {
    case 'insert':
      return <FiPlus size={11} className="text-success" />;
    case 'update':
      return <FiRefreshCw size={11} className="text-warning" />;
    case 'delete':
      return <FiTrash2 size={11} className="text-danger" />;
  }
};

const SidePicker: React.FC<{
  label: string;
  value: { connId: string; database: string; schema: string };
  onChange: (next: { connId: string; database: string; schema: string }) => void;
  connections: Connection[];
  connectedIds: string[];
}> = ({ label, value, onChange, connections, connectedIds }) => (
  <div className="space-y-1.5">
    <div className="text-2xs font-semibold uppercase tracking-wide text-muted">{label}</div>
    <select
      className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink focus:outline-none focus:border-accent"
      value={value.connId}
      onChange={e => onChange({ connId: e.target.value, database: '', schema: '' })}
      aria-label={`${label}连接`}
    >
      <option value="">选择连接…</option>
      {connections.map(c => (
        <option key={c.id} value={c.id} disabled={!connectedIds.includes(c.id)}>
          {c.name}
          {connectedIds.includes(c.id) ? '' : '（未连接）'}
        </option>
      ))}
    </select>
    <div className="flex gap-1.5">
      <input
        className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink focus:outline-none focus:border-accent"
        value={value.database}
        onChange={e => onChange({ ...value, database: e.target.value })}
        placeholder="数据库（可留空）"
        aria-label={`${label}数据库`}
      />
      <input
        className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink focus:outline-none focus:border-accent"
        value={value.schema}
        onChange={e => onChange({ ...value, schema: e.target.value })}
        placeholder="模式 / schema"
        aria-label={`${label}模式`}
      />
    </div>
  </div>
);

// W6: 行级数据同步.
//
// The dialog's job is to make the DELETE count impossible to miss before
// anything runs: a row-level sync can remove every row of the target, so the
// plan lists each change with its operation, deletions are highlighted, and the
// confirm names the worst case. Nothing is ticked on the user's behalf — the
// changes arrive unticked and apply needs an explicit selection.
const RowSyncDialog: React.FC<RowSyncDialogProps> = ({
  connections,
  connectedIds,
  initialSource,
  confirm,
  onClose,
}) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [source, setSource] = useState(initialSource ?? { connId: '', database: '', schema: '' });
  const [target, setTarget] = useState({ connId: '', database: '', schema: '' });
  const [table, setTable] = useState('');
  const [ignoreText, setIgnoreText] = useState('');
  const [loading, setLoading] = useState(false);
  const [applying, setApplying] = useState(false);
  const [preview, setPreview] = useState<RowSyncPreview | null>(null);
  const [result, setResult] = useState<RowSyncApplyResult | null>(null);
  const [error, setError] = useState('');
  // The ticked change seqs. A fresh analysis starts with NOTHING ticked: the
  // cheap-looking default (tick all) is the destructive one.
  const [selected, setSelected] = useState<Set<number>>(new Set());

  const canPreview =
    !!source.connId && !!target.connId && !!table.trim() && source.connId !== target.connId;

  const buildRequest = (): RowSyncRequest => ({
    source,
    target,
    table: table.trim(),
    ignoreColumns: ignoreText
      .split(',')
      .map(s => s.trim())
      .filter(Boolean),
  });

  const runPreview = async () => {
    setError('');
    setResult(null);
    setPreview(null);
    setSelected(new Set());
    setLoading(true);
    try {
      const p = await PreviewRowSync(buildRequest());
      setPreview(p);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };

  // A stale plan is worse than none: changing either side invalidates it.
  useEffect(() => {
    if (preview) setPreview(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source.connId, source.database, source.schema, target.connId, target.database, target.schema, table]);

  const toggle = (seq: number) => {
    setSelected(current => {
      const next = new Set(current);
      if (next.has(seq)) next.delete(seq);
      else next.add(seq);
      return next;
    });
  };

  const selectAll = (op?: RowSyncChange['op']) => {
    if (!preview) return;
    const next = new Set<number>();
    for (const c of preview.sample ?? []) {
      if (!op || c.op === op) next.add(c.seq);
    }
    setSelected(next);
  };

  // The sample is bounded, so "select all" can only ever tick what is visible.
  // Say so rather than silently applying a subset of what the user meant.
  const sampleOnly = !!preview?.truncated;

  const runApply = async () => {
    if (!preview || selected.size === 0) return;
    const changes = (preview.sample ?? []).filter(c => selected.has(c.seq));
    const deletes = changes.filter(c => c.op === 'delete').length;
    const message = [
      `将对目标表 ${preview.table} 应用 ${changes.length} 条变更。`,
      deletes > 0 ? `\n其中 ${deletes} 条是删除——目标表里这些行会被移除。` : '',
      '\n整批在一个事务内执行，任何一条失败都会回滚全部。',
      sampleOnly ? '\n注意：计划超过预览上限，只能勾选列出的部分。' : '',
    ].join('');
    const ok = await confirm({
      title: '应用行级同步',
      message,
      confirmLabel: '应用变更',
      destructive: deletes > 0,
    });
    if (!ok) return;

    setApplying(true);
    setError('');
    try {
      const res = await ApplyRowSync({
        planId: preview.planId,
        seqs: [...selected],
        confirm: true,
      });
      setResult(res);
      if (res.error) {
        toast.error(res.error, { duration: 10000 });
      } else {
        toast.success(`已应用 ${res.applied} 条变更 · ${res.elapsed}`);
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setApplying(false);
    }
  };

  const changes = preview?.sample ?? [];
  const pkLabel = (preview?.pk ?? []).join('·');

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="rowsync-dialog-title"
        className="bg-panel rounded-lg w-full max-w-3xl max-h-[88vh] flex flex-col shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center px-5 py-3 border-b border-line shrink-0">
          <FiRefreshCw size={15} className="mr-2 text-accent" />
          <h2 id="rowsync-dialog-title" className="text-sm font-semibold">
            行级数据同步
          </h2>
          <button className="ml-auto text-secondary hover:text-ink" onClick={onClose} aria-label="关闭行级同步">
            <FiX size={16} />
          </button>
        </div>

        <div className="flex-1 overflow-auto p-5 space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <SidePicker label="源（基准）" value={source} onChange={setSource} connections={connections} connectedIds={connectedIds} />
            <SidePicker label="目标（将被改写）" value={target} onChange={setTarget} connections={connections} connectedIds={connectedIds} />
          </div>

          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2">
              <label htmlFor="rowsync-table" className="block text-2xs text-secondary mb-1">
                表名（两侧同名）
              </label>
              <input
                id="rowsync-table"
                className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink font-mono focus:outline-none focus:border-accent"
                value={table}
                onChange={e => setTable(e.target.value)}
                placeholder="orders"
              />
            </div>
            <div>
              <label htmlFor="rowsync-ignore" className="block text-2xs text-secondary mb-1">
                忽略列（逗号分隔）
              </label>
              <input
                id="rowsync-ignore"
                className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink focus:outline-none focus:border-accent"
                value={ignoreText}
                onChange={e => setIgnoreText(e.target.value)}
                placeholder="updated_at"
              />
            </div>
          </div>

          <div className="flex items-center gap-2">
            <button
              className="px-3 py-1.5 rounded text-xs bg-raised hover:bg-emphasis text-ink border border-line disabled:opacity-40"
              onClick={() => void runPreview()}
              disabled={!canPreview || loading}
            >
              {loading ? '分析中…' : '分析（不改动目标）'}
            </button>
            {preview && (
              <button
                className="px-3 py-1.5 rounded text-xs bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-50"
                onClick={() => void runApply()}
                disabled={applying || selected.size === 0}
              >
                {applying ? '应用中…' : `应用选中的 ${selected.size} 条`}
              </button>
            )}
            {loading && <FiLoader size={13} className="animate-spin text-muted" />}
          </div>

          {error && (
            <p className="text-xs text-danger break-words" role="alert">
              {error}
            </p>
          )}

          {preview && !result && (
            <div className="space-y-3">
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
                <span className="text-secondary">
                  主键 <span className="font-mono text-ink">{pkLabel || '（无）'}</span>
                </span>
                <span className="text-success">新增 {preview.inserts}</span>
                <span className="text-warning">修改 {preview.updates}</span>
                <span className="text-danger">删除 {preview.deletes}</span>
                <span className="text-muted">
                  源 {preview.sourceRows} 行 · 目标 {preview.targetRows} 行
                </span>
              </div>

              {preview.total === 0 && (
                <p className="text-xs text-success flex items-center gap-1">
                  <FiCheck size={12} /> 两侧已经一致，没有需要同步的变更。
                </p>
              )}

              {preview.total > 0 && (
                <>
                  <div className="flex items-center gap-2 text-2xs">
                    <button className="text-accent hover:underline" onClick={() => selectAll()}>
                      全选可见
                    </button>
                    <button className="text-accent hover:underline" onClick={() => setSelected(new Set())}>
                      清空
                    </button>
                    {preview.deletes > 0 && (
                      <button className="text-accent hover:underline" onClick={() => selectAll('delete')}>
                        只选删除
                      </button>
                    )}
                    {sampleOnly && (
                      <span className="text-warning flex items-center gap-1">
                        <FiAlertTriangle size={11} />
                        共 {preview.total} 条，列表只显示前 {changes.length} 条
                      </span>
                    )}
                  </div>

                  <div className="border border-line rounded overflow-hidden">
                    <table className="w-full text-xs">
                      <thead className="bg-raised text-2xs uppercase tracking-wide text-muted">
                        <tr>
                          <th className="w-8 px-2 py-1" />
                          <th className="text-left px-2 py-1 font-medium">操作</th>
                          <th className="text-left px-2 py-1 font-medium">主键</th>
                          <th className="text-left px-2 py-1 font-medium">值</th>
                        </tr>
                      </thead>
                      <tbody>
                        {changes.map(c => (
                          <tr
                            key={c.seq}
                            className={`border-t border-line ${
                              c.op === 'delete' ? 'bg-danger/5' : ''
                            }`}
                          >
                            <td className="px-2 py-1">
                              <input
                                type="checkbox"
                                checked={selected.has(c.seq)}
                                onChange={() => toggle(c.seq)}
                                aria-label={`选择变更 ${c.seq}`}
                              />
                            </td>
                            <td className="px-2 py-1">
                              <span className="flex items-center gap-1">
                                {opIcon(c.op)}
                                {opLabel[c.op]}
                              </span>
                            </td>
                            <td className="px-2 py-1 font-mono text-ink">{c.key}</td>
                            <td className="px-2 py-1 text-2xs text-secondary break-all">
                              {c.values
                                ? Object.entries(c.values)
                                    .map(([k, v]) => `${k}=${v === null ? 'NULL' : String(v)}`)
                                    .join(' · ')
                                : '—'}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </>
              )}
            </div>
          )}

          {result && (
            <div className="space-y-2">
              <p className={`text-xs font-semibold ${result.error ? 'text-danger' : 'text-success'}`}>
                {result.error ? '应用中止' : '应用完成'} · {result.applied}/{result.total} 条 · {result.elapsed}
              </p>
              {result.error && <p className="text-xs text-danger break-words">{result.error}</p>}
              <p className="text-2xs text-muted">
                新增 {result.inserts} · 修改 {result.updates} · 删除 {result.deletes}
              </p>
            </div>
          )}
        </div>

        <div className="flex justify-end gap-2 px-5 py-3 border-t border-line shrink-0">
          <button className="px-3 py-1.5 rounded text-xs bg-raised hover:bg-emphasis text-ink" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
    </div>
  );
};

export default RowSyncDialog;
