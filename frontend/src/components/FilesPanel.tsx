import React, { useCallback, useEffect, useState } from 'react';
import {
  FiCornerUpLeft,
  FiDownload,
  FiFile,
  FiFolder,
  FiPlus,
  FiUpload,
  FiX,
} from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  CloseFilesSession,
  DownloadFile,
  FileEntry,
  ListFiles,
  MkdirFiles,
  OpenFilesSession,
  RemoveFiles,
  RenameFiles,
  UploadFile,
} from '../lib/workbench';

// W-F1: the files panel — one SFTP session, one directory listing, one action
// bar. What it is NOT (recorded honestly): no dual-pane drag-and-drop yet, no
// transfer progress bars (a transfer is one HTTP round trip), no remote-local
// diff. The panel proves the MECHANISM (session lifecycle, realm enforcement,
// atomic staging); richer UX rides the same calls.
//
// The connection form is the same shape the terminal panel uses; the REALM
// (roots) input is this panel's addition: an empty value means "no limits"
// (loopback dev), a path restricts the whole session to that subtree — and the
// refusal messages (host gate + plugin gate) surface verbatim in the toasts.

interface FilesPanelProps {
  onClose: () => void;
}

const FilesPanel: React.FC<FilesPanelProps> = ({ onClose }) => {
  const [session, setSession] = useState('');
  const [dir, setDir] = useState('./');
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [form, setForm] = useState({ host: '127.0.0.1', port: 22, user: '', password: '', roots: '' });
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(
    async (sess: string, d: string) => {
      try {
        const res = await ListFiles(sess, d);
        setEntries(res.entries ?? []);
        setDir(d);
      } catch (e) {
        toast.error((e as Error).message);
      }
    },
    [],
  );

  const openSession = async () => {
    setBusy(true);
    try {
      const roots = form.roots.trim() === '' ? undefined : form.roots.split(',').map(s => s.trim()).filter(Boolean);
      const res = await OpenFilesSession({
        host: form.host,
        port: form.port,
        user: form.user,
        password: form.password,
        roots,
      });
      setSession(res.sessionId);
      await refresh(res.sessionId, './');
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const closeSession = async () => {
    if (!session) return;
    try {
      await CloseFilesSession(session);
      setSession('');
      setEntries([]);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const enter = (e: FileEntry) => {
    if (!e.isDir) return;
    void refresh(session, joinPath(dir, e.name));
  };

  const up = () => {
    const trimmed = dir.replace(/\/+$/, '');
    const idx = trimmed.lastIndexOf('/');
    void refresh(session, idx <= 0 ? './' : trimmed.slice(0, idx + 1) || './');
  };

  const mkdir = async () => {
    const name = window.prompt('新目录名：');
    if (!name) return;
    try {
      await MkdirFiles(session, joinPath(dir, name));
      await refresh(session, dir);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const rename = async (e: FileEntry) => {
    const to = window.prompt(`重命名 ${e.name} 为：`, e.name);
    if (!to || to === e.name) return;
    try {
      await RenameFiles(session, joinPath(dir, e.name), joinPath(dir, to));
      await refresh(session, dir);
    } catch (e2) {
      toast.error((e2 as Error).message);
    }
  };

  const remove = async (e: FileEntry) => {
    if (!window.confirm(`删除 ${e.isDir ? '目录' : '文件'} ${e.name}？`)) return;
    try {
      await RemoveFiles(session, joinPath(dir, e.name));
      await refresh(session, dir);
    } catch (e2) {
      toast.error((e2 as Error).message);
    }
  };

  const upload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []);
    if (files.length === 0) return;
    setBusy(true);
    try {
      for (const f of files) {
        // The local path is the browser-reported name resolved by the HOST side
        // (desktop app: page and process share the machine). The remote target
        // is the current directory + the file's name.
        const res = await UploadFile(session, f.name, joinPath(dir, f.name));
        toast.success(`已上传 ${res.path}（${res.size} 字节）`);
      }
      await refresh(session, dir);
    } catch (e2) {
      toast.error((e2 as Error).message);
    } finally {
      setBusy(false);
      e.target.value = '';
    }
  };

  const download = async (e: FileEntry) => {
    if (e.isDir) return;
    try {
      const local = `${e.name}`;
      const res = await DownloadFile(session, joinPath(dir, e.name), local);
      toast.success(`已下载 ${res.size} 字节 → ${res.path}`);
    } catch (e2) {
      toast.error((e2 as Error).message);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="文件工作台"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-4xl h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-3 py-2 border-b border-line">
          <FiFolder size={14} className="text-accent shrink-0" />
          <span className="text-sm font-medium text-ink flex-1">
            文件 {session && <span className="text-2xs text-muted">（会话 {session}）</span>}
          </span>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        {!session ? (
          <div className="p-6 space-y-3">
            <div className="grid grid-cols-2 gap-2 text-2xs">
              <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="主机"
                value={form.host} onChange={e => setForm(f => ({ ...f, host: e.target.value }))} aria-label="主机" />
              <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="端口"
                type="number" value={form.port} onChange={e => setForm(f => ({ ...f, port: Number(e.target.value) }))}
                aria-label="端口" />
              <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="用户"
                value={form.user} onChange={e => setForm(f => ({ ...f, user: e.target.value }))} aria-label="用户" />
              <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="密码"
                type="password" value={form.password} onChange={e => setForm(f => ({ ...f, password: e.target.value }))}
                aria-label="密码" />
            </div>
            <input className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-2xs text-ink"
              placeholder="允许的路径（白名单 realm，逗号分隔；留空 = 不限制）"
              value={form.roots} onChange={e => setForm(f => ({ ...f, roots: e.target.value }))} aria-label="路径白名单" />
            <button
              className="w-full py-1.5 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              onClick={() => void openSession()}
              disabled={busy || form.host === '' || form.user === ''}
            >
              {busy ? '连接中…' : '打开 SFTP 会话'}
            </button>
            <p className="text-2xs text-muted leading-relaxed">
              会话只能访问白名单内的路径（越界会被宿主与插件双重拒绝）。上传使用「先写 .tmp 再原子
              改名」的两步提交：失败的传输只留下可回收的 .tmp 残留，不会写坏目标文件。
            </p>
          </div>
        ) : (
          <>
            <div className="flex items-center gap-2 px-3 py-1.5 border-b border-line text-2xs">
              <button className="flex items-center gap-1 text-secondary hover:text-ink" onClick={up} aria-label="上一级">
                <FiCornerUpLeft size={11} /> 上一级
              </button>
              <span className="font-mono text-ink flex-1 truncate">{dir}</span>
              <button className="flex items-center gap-1 text-secondary hover:text-ink" onClick={() => void mkdir()}>
                <FiPlus size={11} /> 新建目录
              </button>
              <label className="flex items-center gap-1 text-secondary hover:text-ink cursor-pointer">
                <FiUpload size={11} /> 上传
                <input type="file" multiple className="hidden" onChange={e => void upload(e)} aria-label="上传文件" />
              </label>
              <button className="text-secondary hover:text-danger" onClick={() => void closeSession()} aria-label="断开">
                <FiX size={12} />
              </button>
            </div>
            <div className="flex-1 min-h-0 overflow-auto">
              <table className="w-full text-2xs">
                <thead className="bg-raised text-muted uppercase tracking-wide sticky top-0">
                  <tr>
                    <th className="text-left px-2 py-1.5 font-medium">名称</th>
                    <th className="text-right px-2 py-1.5 font-medium">大小</th>
                    <th className="text-left px-2 py-1.5 font-medium">修改时间</th>
                    <th className="text-right px-2 py-1.5 font-medium">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {entries.map(en => (
                    <tr key={en.name} className="border-t border-line hover:bg-raised">
                      <td className="px-2 py-1">
                        {en.isDir ? (
                          <button className="flex items-center gap-1.5 text-ink hover:text-accent" onClick={() => enter(en)}>
                            <FiFolder size={11} className="text-warning" /> {en.name}
                          </button>
                        ) : (
                          <span className="flex items-center gap-1.5 text-ink">
                            <FiFile size={11} className="text-secondary" /> {en.name}
                          </span>
                        )}
                      </td>
                      <td className="px-2 py-1 text-right text-muted font-mono">{en.isDir ? '—' : en.size}</td>
                      <td className="px-2 py-1 text-muted">{en.modTime?.replace('T', ' ').slice(0, 19)}</td>
                      <td className="px-2 py-1 text-right whitespace-nowrap">
                        {!en.isDir && (
                          <button className="text-secondary hover:text-accent mr-2" onClick={() => void download(en)}
                            title="下载" aria-label={`下载 ${en.name}`}>
                            <FiDownload size={11} />
                          </button>
                        )}
                        <button className="text-secondary hover:text-ink mr-2" onClick={() => void rename(en)}
                          title="重命名" aria-label={`重命名 ${en.name}`}>
                          重命名
                        </button>
                        <button className="text-secondary hover:text-danger" onClick={() => void remove(en)}
                          title="删除" aria-label={`删除 ${en.name}`}>
                          删除
                        </button>
                      </td>
                    </tr>
                  ))}
                  {entries.length === 0 && (
                    <tr>
                      <td colSpan={4} className="px-2 py-3 text-muted">
                        目录为空。
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </>
        )}
      </div>
    </div>
  );
};

// joinPath is the client's SFTP path join (servers are unix-y).
function joinPath(dir: string, name: string): string {
  const base = dir.replace(/\/+$/, '');
  if (base === '' || base === '.') return `./${name}`;
  return `${base}/${name}`;
}

export default FilesPanel;
