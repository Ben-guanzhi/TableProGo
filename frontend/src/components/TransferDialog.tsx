import React, { useEffect, useMemo, useRef, useState } from 'react';
import { FiAlertTriangle, FiCheck, FiChevronRight, FiLoader, FiShuffle, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  ApplyTransfer,
  PreviewTransfer,
  PreviewTransfer as ApiPreviewTransfer,
  TransferPreview,
  TransferRequest,
  TransferResult,
  TableRef,
} from '../api';
import type { Connection } from '../types';
import { useDialog } from '../dialog';

interface TransferDialogProps {
  connections: Connection[];
  connectedIds: string[];
  // The connection the navigator was on, used as the initial source.
  initialSource?: { connId: string; database: string; schema: string };
  // Confirmation goes through the app's own dialog, so a destructive copy is
  // never confirmed by a silent window.confirm.
  confirm: (request: {
    title: string;
    message: string;
    confirmLabel: string;
    destructive?: boolean;
  }) => Promise<boolean>;
  onClose: () => void;
}

// One side of the copy: which connection, which database, which schema.
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

// W5: 服务器间数据传输.
//
// The dialog's whole job is to make the plan visible BEFORE anything is written:
// which tables, in what order, how many rows, which DDL, and every reason part of
// it is not faithful (a cycle's deferred constraint, a cross-engine type loss).
// Apply is a second, explicit click, and it re-derives the plan on the server.
const TransferDialog: React.FC<TransferDialogProps> = ({
  connections,
  connectedIds,
  initialSource,
  confirm,
  onClose,
}) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [source, setSource] = useState(
    initialSource ?? { connId: '', database: '', schema: '' },
  );
  const [target, setTarget] = useState({ connId: '', database: '', schema: '' });
  const [ignoreForeignKeys, setIgnoreForeignKeys] = useState(false);
  const [skipData, setSkipData] = useState(false);
  const [loading, setLoading] = useState(false);
  const [applying, setApplying] = useState(false);
  const [preview, setPreview] = useState<TransferPreview | null>(null);
  const [result, setResult] = useState<TransferResult | null>(null);
  const [error, setError] = useState('');
  // The statement list is long; it starts collapsed so the plan stays readable.
  const [showDDL, setShowDDL] = useState(false);

  const canPreview = !!source.connId && !!target.connId && source.connId !== target.connId;

  const buildRequest = (): TransferRequest => ({
    source,
    target,
    tables: [],
    ignoreForeignKeys,
    skipData,
  });

  const runPreview = async () => {
    setError('');
    setResult(null);
    setLoading(true);
    try {
      const p = await PreviewTransfer(buildRequest());
      setPreview(p);
    } catch (e) {
      setPreview(null);
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };

  // Re-preview when the scope changes: a plan shown against a different target is
  // worse than no plan, because it looks authoritative.
  useEffect(() => {
    if (preview) setPreview(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source.connId, source.database, source.schema, target.connId, target.database, target.schema,
    ignoreForeignKeys, skipData]);

  const runApply = async () => {
    if (!preview) return;
    const losses = preview.losses ?? [];
    const warnings = preview.warnings ?? [];
    const message = [
      `将把 ${preview.tables?.length ?? 0} 张表（约 ${preview.totalRows} 行）复制到目标连接。`,
      warnings.length ? `\n${warnings.length} 条警告。` : '',
      losses.length ? `\n${losses.length} 处类型不保真，可能丢精度。` : '',
      '\n每张表在一个事务内写入；任何一张失败都会停止。',
    ].join('');
    const ok = await confirm({
      title: '开始传输',
      message,
      confirmLabel: '开始传输',
      destructive: losses.length > 0 || warnings.length > 0,
    });
    if (!ok) return;

    setApplying(true);
    setError('');
    try {
      const res = await ApplyTransfer(buildRequest());
      setResult(res);
      if (res.aborted) {
        toast.error(res.error ?? '传输中止', { duration: 10000 });
      } else {
        toast.success(`传输完成：${res.rows} 行 · ${res.elapsed}`);
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setApplying(false);
    }
  };

  const tables = preview?.tables ?? [];
  const warnings = preview?.warnings ?? [];
  const losses = preview?.losses ?? [];
  const statements = preview?.statements ?? [];
  const cycles = preview?.cycles ?? [];
  const ddl = useMemo(() => statements.map(s => s.sql).join(';\n\n'), [statements]);

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="transfer-dialog-title"
        className="bg-panel rounded-lg w-full max-w-3xl max-h-[88vh] flex flex-col shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center px-5 py-3 border-b border-line shrink-0">
          <FiShuffle size={15} className="mr-2 text-accent" />
          <h2 id="transfer-dialog-title" className="text-sm font-semibold">
            数据传输
          </h2>
          <button
            className="ml-auto text-secondary hover:text-ink"
            onClick={onClose}
            aria-label="关闭数据传输"
          >
            <FiX size={16} />
          </button>
        </div>

        <div className="flex-1 overflow-auto p-5 space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <SidePicker
              label="源"
              value={source}
              onChange={setSource}
              connections={connections}
              connectedIds={connectedIds}
            />
            <SidePicker
              label="目标"
              value={target}
              onChange={setTarget}
              connections={connections}
              connectedIds={connectedIds}
            />
          </div>
          {source.connId && source.connId === target.connId && source.database === target.database && (
            <p className="text-xs text-danger">源与目标是同一个库，这样会把数据复制到它自己身上。</p>
          )}

          <div className="flex flex-wrap gap-4 text-xs text-secondary">
            <label className="flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={skipData}
                onChange={e => setSkipData(e.target.checked)}
              />
              仅复制结构（不传数据）
            </label>
            <label className="flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={ignoreForeignKeys}
                onChange={e => setIgnoreForeignKeys(e.target.checked)}
              />
              不复制外键
            </label>
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
                disabled={applying}
              >
                {applying ? '传输中…' : `开始传输（${tables.length} 张表）`}
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
              <div className="text-xs text-secondary">
                共 <span className="text-ink font-semibold">{tables.length}</span> 张表，约{' '}
                <span className="text-ink font-semibold">{preview.totalRows}</span> 行，按外键依赖顺序复制。
              </div>

              <div className="border border-line rounded overflow-hidden">
                <table className="w-full text-xs">
                  <thead className="bg-raised text-2xs uppercase tracking-wide text-muted">
                    <tr>
                      <th className="text-left px-2 py-1 font-medium">#</th>
                      <th className="text-left px-2 py-1 font-medium">表</th>
                      <th className="text-right px-2 py-1 font-medium">行数</th>
                      <th className="text-left px-2 py-1 font-medium">备注</th>
                    </tr>
                  </thead>
                  <tbody>
                    {tables.map((t, i) => (
                      <tr key={t.source} className="border-t border-line">
                        <td className="px-2 py-1 text-muted">{i + 1}</td>
                        <td className="px-2 py-1 font-mono text-ink">{t.source}</td>
                        <td className="px-2 py-1 text-right text-secondary">
                          {t.rows < 0 ? '未知' : t.rows}
                        </td>
                        <td className="px-2 py-1 text-2xs text-warning">
                          {t.deferred ? '循环外键：约束在数据之后' : ''}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {cycles.length > 0 && (
                <p className="text-2xs text-warning">
                  检测到 {cycles.length} 组循环引用：这些表的外键会在数据写入之后补建
                  （目标引擎若不支持事后添加外键，会明确跳过并告警）。
                </p>
              )}

              {warnings.length > 0 && (
                <div className="border border-warning/40 bg-warning/10 rounded p-2 space-y-1">
                  {warnings.map((w, i) => (
                    <p key={i} className="text-2xs text-warning flex items-start gap-1">
                      <FiAlertTriangle size={11} className="mt-0.5 shrink-0" />
                      <span className="break-words">{w}</span>
                    </p>
                  ))}
                </div>
              )}

              {losses.length > 0 && (
                <div className="border border-danger/40 bg-danger/10 rounded p-2 space-y-1">
                  <p className="text-2xs font-semibold text-danger">类型不保真（会丢精度或截断）</p>
                  {losses.map((l, i) => (
                    <p key={i} className="text-2xs text-danger break-words">
                      {l.Table}.{l.Column}: {l.Source} → {l.Target} — {l.Reason}
                    </p>
                  ))}
                </div>
              )}

              {statements.length > 0 && (
                <div>
                  <button
                    className="text-2xs text-accent hover:underline flex items-center gap-1"
                    onClick={() => setShowDDL(v => !v)}
                    aria-expanded={showDDL}
                  >
                    <FiChevronRight
                      size={11}
                      className={showDDL ? 'rotate-90 transition-transform' : 'transition-transform'}
                    />
                    {showDDL ? '收起' : '查看'}将执行的 DDL（{statements.length} 条）
                  </button>
                  {showDDL && (
                    <pre className="mt-1 max-h-56 overflow-auto bg-canvas border border-line rounded p-2 text-2xs font-mono text-ink whitespace-pre-wrap break-words">
                      {ddl}
                    </pre>
                  )}
                </div>
              )}
            </div>
          )}

          {result && (
            <div className="space-y-2">
              <p className={`text-xs font-semibold ${result.aborted ? 'text-danger' : 'text-success'}`}>
                {result.aborted ? '传输中止' : '传输完成'} · {result.rows} 行 · {result.elapsed}
              </p>
              {result.error && <p className="text-xs text-danger break-words">{result.error}</p>}
              <div className="border border-line rounded overflow-hidden">
                <table className="w-full text-xs">
                  <thead className="bg-raised text-2xs uppercase tracking-wide text-muted">
                    <tr>
                      <th className="text-left px-2 py-1 font-medium">表</th>
                      <th className="text-right px-2 py-1 font-medium">读</th>
                      <th className="text-right px-2 py-1 font-medium">写</th>
                      <th className="text-left px-2 py-1 font-medium">结果</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(result.tables ?? []).map(t => (
                      <tr key={t.source} className="border-t border-line">
                        <td className="px-2 py-1 font-mono text-ink">{t.source}</td>
                        <td className="px-2 py-1 text-right text-secondary">{t.rowsRead}</td>
                        <td className="px-2 py-1 text-right text-secondary">{t.rowsWritten}</td>
                        <td className="px-2 py-1 text-2xs">
                          {t.error ? (
                            <span className="text-danger break-words">{t.error}</span>
                          ) : (
                            <span className="text-success flex items-center gap-1">
                              <FiCheck size={11} /> 完成
                            </span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </div>

        <div className="flex justify-end gap-2 px-5 py-3 border-t border-line shrink-0">
          <button
            className="px-3 py-1.5 rounded text-xs bg-raised hover:bg-emphasis text-ink"
            onClick={onClose}
          >
            关闭
          </button>
        </div>
      </div>
    </div>
  );
};

export default TransferDialog;
