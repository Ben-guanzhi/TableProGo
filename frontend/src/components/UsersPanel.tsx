import React, { useEffect, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import { FiUserPlus } from 'react-icons/fi';
import {
  GetUserList,
  GetUserDDL,
  ExecuteQuery,
} from '../api';
import { useDialog } from '../dialog';

// UsersPanel（Navicat「用户」/ DBeaver Users 视图）：列出可登录用户，
// 创建/授权/删除经后端生成的方言 DDL——**语句先显示再执行**（Safe Mode
// 与只读锁全部保留，与内核 UserAdmin 的「执行归客户端」分工一致）。

interface UsersPanelProps {
  connId: string;
  database: string;
  onClose: () => void;
}

const UsersPanel: React.FC<UsersPanelProps> = ({ connId, database, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [users, setUsers] = useState<string[]>([]);
  const [hosts, setHosts] = useState<string[]>([]);
  const [engineOK, setEngineOK] = useState(true);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [newUser, setNewUser] = useState('');
  const [newHost, setNewHost] = useState('%');
  const [newPass, setNewPass] = useState('');
  const [busy, setBusy] = useState(false);

  const reload = () => {
    setLoading(true);
    GetUserList(connId, database)
      .then(res => {
        setUsers(res.users ?? []);
        setHosts(res.hosts ?? []);
        setEngineOK(res.engineOk);
        setError(res.error ?? '');
      })
      .catch(e => setError((e as Error).message))
      .finally(() => setLoading(false));
  };

  useEffect(reload, []);

  // runDDL 把后端生成的语句显示在确认框里，确认后经 ExecuteQuery 执行。
  const runDDL = async (action: 'create' | 'drop' | 'grant', user: string, host: string, pass: string, grantDb: string) => {
    setBusy(true);
    try {
      const res = await GetUserDDL(connId, database, action, user, host, pass, grantDb);
      // eslint-disable-next-line no-alert
      const ok = window.confirm(`将执行：\n\n${res.sql}\n\n确认？`);
      if (!ok) return;
      const r = await ExecuteQuery(connId, res.sql, database);
      if (r.error) throw new Error(r.error);
      toast.success(`${action} 完成`);
      reload();
    } catch (e) {
      toast.error((e as Error).message, { duration: 8000 });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      ref={panelRef}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40"
      onPointerDown={e => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div className="w-[520px] bg-panel border border-line-strong rounded-md shadow-xl p-4 text-xs text-ink max-h-[80vh] overflow-auto">
        <div className="flex items-center gap-2 mb-3">
          <FiUserPlus size={14} className="text-accent" />
          <span className="text-sm font-semibold">用户管理</span>
          <button className="ml-auto text-secondary hover:text-ink" onClick={onClose} aria-label="关闭用户管理">×</button>
        </div>
        {!engineOK && <div className="text-warning mb-3">{error || '当前引擎不支持用户管理'}</div>}
        {error && engineOK && <div className="text-danger mb-3">{error}</div>}
        {engineOK && !error && (
          <>
            <ul className="mb-3 border border-line rounded divide-y divide-line-soft">
              {loading && <li className="px-2 py-1 text-muted">读取中…</li>}
              {users.map((u, i) => (
                <li key={`${u}@${hosts[i] ?? ''}`} className="flex items-center px-2 py-1">
                  <span className="font-mono">{u}{hosts[i] ? `@${hosts[i]}` : ''}</span>
                  <button
                    className="ml-auto text-danger hover:underline"
                    disabled={busy}
                    onClick={() => void runDDL('drop', u, hosts[i] ?? '', '', '')}
                    aria-label={`删除用户 ${u}`}
                  >
                    删除
                  </button>
                </li>
              ))}
            </ul>
            <div className="grid grid-cols-3 gap-2 mb-2">
              <input value={newUser} onChange={e => setNewUser(e.target.value)} placeholder="用户名"
                className="bg-canvas border border-line-strong rounded px-2 py-1" aria-label="新用户名" />
              <input value={newHost} onChange={e => setNewHost(e.target.value)} placeholder="host"
                className="bg-canvas border border-line-strong rounded px-2 py-1" aria-label="主机" />
              <input type="password" value={newPass} onChange={e => setNewPass(e.target.value)} placeholder="密码"
                className="bg-canvas border border-line-strong rounded px-2 py-1" aria-label="密码" />
            </div>
            <div className="flex gap-2">
              <button
                disabled={busy || !newUser}
                onClick={() => void runDDL('create', newUser, newHost, newPass, '')}
                className="px-2.5 py-1 rounded text-xs bg-accent text-on-accent disabled:opacity-40"
              >
                创建用户
              </button>
              <button
                disabled={busy || !newUser}
                onClick={() => void runDDL('grant', newUser, newHost, '', database)}
                className="px-2.5 py-1 rounded text-xs border border-line-strong hover:bg-canvas disabled:opacity-40"
                aria-label="授予当前库全部权限"
              >
                授予本库权限
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
};

export default UsersPanel;
