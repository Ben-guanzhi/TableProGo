import React, { useState } from 'react';
import { FiPackage, FiRefreshCw, FiUpload, FiDownload, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import { ExtensionRow, FetchExtensions, InstallExtension, SyncPull, SyncPush } from '../lib/workbench';

// W-S1: the extensions page and the team-sync controls.
//
// Three facts the page states rather than hides:
//   1. THE CATALOG IS A JSON FILE on a Git raw URL or a WebDAV share — the same
//      fetcher covers both because both serve JSON over HTTP.
//   2. INSTALL IS DIGEST-VERIFIED: a mismatch is refused and leaves nothing behind.
//      A digest is INTEGRITY, not AUTHENTICITY — signing needs a key distribution
//      story that this page does not pretend to have.
//   3. SYNC CARRIES NO SECRETS: the document has the plugin MANIFEST and the
//      connection ARCHIVE; a password never leaves the machine (the host scrubs it
//      by shape, so it does not depend on remembering).
//
// Version linking is peer-dependency: a plugin declares "key ≥N" and the host
// refuses to activate against an older provider. Multi-version coexistence is NOT
// supported — that is a documented limitation, not a bug report.

const ExtensionsPanel: React.FC<{ onClose: () => void }> = ({ onClose }) => {
  const [catalogUrl, setCatalogUrl] = useState('');
  const [syncUrl, setSyncUrl] = useState('');
  const [rows, setRows] = useState<ExtensionRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');

  const loadCatalog = async () => {
    if (catalogUrl.trim() === '') return;
    setBusy(true);
    try {
      const res = await FetchExtensions(catalogUrl.trim());
      setRows(res.rows ?? []);
      setNotice(`已读取目录：${res.rows?.length ?? 0} 个条目。`);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const install = async (row: ExtensionRow) => {
    setBusy(true);
    try {
      const res = await InstallExtension(catalogUrl.trim(), row.id);
      setNotice(res.notice);
      toast.success(`已安装 ${row.id} ${row.version}`);
      await loadCatalog();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const pull = async () => {
    if (syncUrl.trim() === '') return;
    setBusy(true);
    try {
      const res = await SyncPull(syncUrl.trim());
      const n = res.document?.connections?.length ?? 0;
      const p = res.document?.plugins?.length ?? 0;
      setNotice(
        `已拉取：${n} 个连接档案、${p} 个插件清单。${
          res.conflict ? `本地较旧，远端胜出；旧文档已保存为冲突副本 ${res.conflictCopy ?? ''}` : '本地较新，未被覆盖。'
        }`,
      );
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const push = async () => {
    if (syncUrl.trim() === '') return;
    setBusy(true);
    try {
      await SyncPush(syncUrl.trim());
      setNotice('已推送：只有插件清单与连接档案（秘密为 ref，不外传）。');
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="扩展"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-4xl max-h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-3 py-2 border-b border-line">
          <FiPackage size={14} className="text-accent shrink-0" />
          <span className="text-sm font-medium text-ink flex-1">扩展与团队同步</span>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        <div className="p-3 space-y-3 overflow-auto">
          <div className="flex items-center gap-2 text-2xs">
            <input className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink"
              placeholder="目录地址（Git raw / WebDAV 的 JSON，例如 https://…/catalog.json）"
              value={catalogUrl} onChange={e => setCatalogUrl(e.target.value)} aria-label="目录地址" />
            <button className="flex items-center gap-1 px-2 py-1.5 rounded border border-line text-secondary hover:text-ink disabled:opacity-40"
              onClick={() => void loadCatalog()} disabled={busy || catalogUrl.trim() === ''}>
              <FiRefreshCw size={10} /> 读取目录
            </button>
          </div>

          <div className="border border-line rounded overflow-hidden">
            <table className="w-full text-2xs">
              <thead className="bg-raised text-muted uppercase tracking-wide">
                <tr>
                  <th className="text-left px-2 py-1.5 font-medium">插件</th>
                  <th className="text-left px-2 py-1.5 font-medium">版本</th>
                  <th className="text-left px-2 py-1.5 font-medium">状态</th>
                  <th className="text-right px-2 py-1.5 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {rows.map(r => (
                  <tr key={r.id} className="border-t border-line">
                    <td className="px-2 py-1">
                      <div className="text-ink">{r.name || r.id}</div>
                      {r.description && <div className="text-muted">{r.description}</div>}
                    </td>
                    <td className="px-2 py-1 font-mono text-muted">
                      {r.version}
                      {r.state === 'updatable' && r.localVersion && (
                        <span className="text-warning"> （本地 {r.localVersion}）</span>
                      )}
                    </td>
                    <td className="px-2 py-1">
                      <span className={
                        r.state === 'installed' ? 'text-success'
                          : r.state === 'updatable' ? 'text-warning' : 'text-accent'
                      }>
                        {r.state === 'installed' ? '已安装' : r.state === 'updatable' ? '可更新' : '可安装'}
                      </span>
                    </td>
                    <td className="px-2 py-1 text-right">
                      {r.state !== 'installed' && (
                        <button className="px-2 py-0.5 rounded bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
                          onClick={() => void install(r)} disabled={busy}>
                          {r.state === 'updatable' ? '更新' : '安装'}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
                {rows.length === 0 && (
                  <tr>
                    <td colSpan={4} className="px-2 py-3 text-muted">
                      还没有目录。填写地址后点"读取目录"。
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>

          <div className="flex items-center gap-2 text-2xs">
            <input className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink"
              placeholder="团队同步地址（同一份 JSON 的 PUT/GET，WebDAV 或 Git 文件 API）"
              value={syncUrl} onChange={e => setSyncUrl(e.target.value)} aria-label="同步地址" />
            <button className="flex items-center gap-1 px-2 py-1.5 rounded border border-line text-secondary hover:text-ink disabled:opacity-40"
              onClick={() => void pull()} disabled={busy || syncUrl.trim() === ''}>
              <FiDownload size={10} /> 拉取
            </button>
            <button className="flex items-center gap-1 px-2 py-1.5 rounded bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              onClick={() => void push()} disabled={busy || syncUrl.trim() === ''}>
              <FiUpload size={10} /> 推送
            </button>
          </div>

          {notice && <div className="text-2xs text-ink border border-line rounded p-2">{notice}</div>}

          <p className="text-2xs text-muted leading-relaxed">
            安装按 sha256 摘要校验：不匹配即拒绝且不留残余。摘要保证完整性，不保证来源可信——签名需要
            密钥分发，是已知的后续工作。同步只包含插件清单与连接档案：密码等秘密按字段形状被宿主清除，
            永不出本机；冲突按 updatedAt LWW，落败文档保存为冲突副本。版本链接采用 peer-dependency
            （banner 声明键名 + 最低版本，宿主解析），<span className="text-warning">多版本共存明确不支持</span>。
          </p>
        </div>
      </div>
    </div>
  );
};

export default ExtensionsPanel;
