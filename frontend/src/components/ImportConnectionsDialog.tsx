import React, { useRef, useState } from 'react';
import { FiAlertTriangle, FiCheck, FiDownload, FiLoader, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import { ConnImportResult, ImportConnections } from '../api';
import { useDialog } from '../dialog';

interface ImportConnectionsDialogProps {
  onClose: () => void;
  // Called after a successful apply so the sidebar can reload its list.
  onImported?: () => void;
}

// W12: 从别的客户端导入连接.
//
// The dialog is a two-step: read (preview) then apply. That is the same shape the
// transfer and sync dialogs use, and it exists here for the same reason — the
// user should see exactly which connections are about to appear, which rows were
// skipped, and that no password comes along, BEFORE the list changes.
const ImportConnectionsDialog: React.FC<ImportConnectionsDialogProps> = ({ onClose, onImported }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [path, setPath] = useState('');
  const [group, setGroup] = useState('');
  const [loading, setLoading] = useState(false);
  const [applying, setApplying] = useState(false);
  const [preview, setPreview] = useState<ConnImportResult | null>(null);
  const [error, setError] = useState('');

  const runPreview = async () => {
    setError('');
    setPreview(null);
    setLoading(true);
    try {
      setPreview(await ImportConnections({ path: path.trim(), group: group.trim() }));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };

  const runApply = async () => {
    if (!preview || preview.rows === null || preview.rows.length === 0) return;
    setApplying(true);
    setError('');
    try {
      const res = await ImportConnections({
        path: path.trim(),
        group: group.trim(),
        apply: true,
      });
      setPreview(res);
      toast.success(`已导入 ${res.imported} 个连接`);
      onImported?.();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setApplying(false);
    }
  };

  const rows = preview?.rows ?? [];
  const skipped = preview?.skipped ?? [];
  const notes = preview?.notes ?? [];
  const renamed = preview?.renamed ?? [];
  const applied = (preview?.imported ?? 0) > 0;

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="import-conn-title"
        className="bg-panel rounded-lg w-full max-w-3xl max-h-[88vh] flex flex-col shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center px-5 py-3 border-b border-line shrink-0">
          <FiDownload size={15} className="mr-2 text-accent" />
          <h2 id="import-conn-title" className="text-sm font-semibold">
            导入连接
          </h2>
          <button className="ml-auto p-1 text-secondary hover:text-ink" onClick={onClose} aria-label="关闭导入连接">
            <FiX size={16} />
          </button>
        </div>

        <div className="flex-1 overflow-auto p-5 space-y-4">
          <p className="text-2xs text-muted leading-relaxed">
            支持：<span className="text-ink">DBeaver</span> 的 <span className="font-mono">data-sources.json</span>
            （通常在 <span className="font-mono">%APPDATA%\DBeaverData\workspace6\General\.dbeaver\</span>）、
            一行一个连接 URL 的文本、以及 CSV（表头 name/engine/host/port/user/database）。
            <br />
            Navicat 的 <span className="font-mono">.ncx</span> 是加密的，本程序不尝试解密——
            请在 Navicat 连接属性里复制连接 URL，一行一个存成 .txt 再导入。
            <br />
            <span className="text-warning">口令不会被导入</span>：首次连接时填写一次。
          </p>

          <div className="grid grid-cols-4 gap-3">
            <div className="col-span-3">
              <label htmlFor="import-path" className="block text-2xs text-secondary mb-1">
                文件路径
              </label>
              <input
                id="import-path"
                type="text"
                className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink font-mono focus:outline-none focus:border-accent"
                value={path}
                onChange={e => {
                  setPath(e.target.value);
                  setPreview(null);
                }}
                placeholder="C:\Users\me\AppData\Roaming\DBeaverData\workspace6\General\.dbeaver\data-sources.json"
              />
            </div>
            <div>
              <label htmlFor="import-group" className="block text-2xs text-secondary mb-1">
                分组（可选）
              </label>
              <input
                id="import-group"
                type="text"
                className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink focus:outline-none focus:border-accent"
                value={group}
                onChange={e => setGroup(e.target.value)}
                placeholder="迁移"
              />
            </div>
          </div>

          <div className="flex items-center gap-2">
            <button
              className="px-3 py-1.5 rounded text-xs bg-raised hover:bg-emphasis text-ink border border-line disabled:opacity-40"
              onClick={() => void runPreview()}
              disabled={!path.trim() || loading}
            >
              {loading ? '读取中…' : '读取（不写入）'}
            </button>
            {preview && rows.length > 0 && !applied && (
              <button
                className="px-3 py-1.5 rounded text-xs bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-50"
                onClick={() => void runApply()}
                disabled={applying}
              >
                {applying ? '导入中…' : `导入 ${rows.length} 个连接`}
              </button>
            )}
            {loading && <FiLoader size={13} className="animate-spin text-muted" />}
          </div>

          {error && (
            <p className="text-xs text-danger break-words" role="alert">
              {error}
            </p>
          )}

          {preview && (
            <div className="space-y-3">
              {applied && (
                <p className="text-xs text-success flex items-center gap-1">
                  <FiCheck size={12} /> 已导入 {preview.imported} 个连接
                </p>
              )}
              {renamed.length > 0 && (
                <div className="text-2xs text-warning space-y-0.5">
                  <p className="flex items-center gap-1">
                    <FiAlertTriangle size={11} />
                    以下连接重名，已改名而不是覆盖：
                  </p>
                  {renamed.map((r, i) => (
                    <p key={i} className="font-mono pl-4 break-all">{r}</p>
                  ))}
                </div>
              )}

              {rows.length > 0 && (
                <div className="border border-line rounded overflow-hidden">
                  <table className="w-full text-xs">
                    <thead className="bg-raised text-2xs uppercase tracking-wide text-muted">
                      <tr>
                        <th className="text-left px-2 py-1.5 font-medium">名称</th>
                        <th className="text-left px-2 py-1.5 font-medium">引擎</th>
                        <th className="text-left px-2 py-1.5 font-medium">地址</th>
                        <th className="text-left px-2 py-1.5 font-medium">库 / 用户</th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map((r, i) => (
                        <tr key={`${r.name}-${i}`} className="border-t border-line">
                          <td className="px-2 py-1 text-ink">{r.name}</td>
                          <td className="px-2 py-1 text-secondary">{r.engine}</td>
                          <td className="px-2 py-1 font-mono text-2xs break-all">
                            {r.host}
                            {r.port ? `:${r.port}` : ''}
                          </td>
                          <td className="px-2 py-1 text-2xs text-secondary break-all">
                            {r.database || '—'}
                            {r.user ? ` · ${r.user}` : ''}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}

              {skipped.length > 0 && (
                <div className="border border-warning/40 bg-warning/10 rounded p-2 space-y-1">
                  <p className="text-2xs font-semibold text-warning">跳过 {skipped.length} 条</p>
                  {skipped.map((s, i) => (
                    <p key={i} className="text-2xs text-warning break-words">
                      {s.where}：{s.reason}
                    </p>
                  ))}
                </div>
              )}

              {notes.length > 0 && (
                <div className="space-y-1">
                  {notes.map((n, i) => (
                    <p key={i} className="text-2xs text-muted break-words">
                      · {n}
                    </p>
                  ))}
                </div>
              )}
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

export default ImportConnectionsDialog;
