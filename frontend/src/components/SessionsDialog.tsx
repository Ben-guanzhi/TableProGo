import React, { useEffect, useRef, useState } from 'react';
import { FiAlertTriangle, FiRefreshCw, FiX } from 'react-icons/fi';
import { ListServerSessions, KillServerSession, ProcSession } from '../api';
import { useDialog } from '../dialog';

interface SessionsDialogProps {
  connId: string;
  database: string;
  onClose: () => void;
}

// 会话管理（Navicat「会话管理」/ DBeaver Sessions 的对齐）：列出当前服务器的
// 其他会话（内核 ProcessLister），选中可终止。
//
// 终止**两步确认**：第一次点按钮进入确认态（按钮变红 + 提示），再次点击同一
// 选中才真正执行——杀会话让对方未提交事务回滚，不一键直发。引擎不支持时
// 面板给一行说明（不是空表）。
const SessionsDialog: React.FC<SessionsDialogProps> = ({ connId, database, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [rows, setRows] = useState<ProcSession[] | null>(null);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState('');
  const [pendingKill, setPendingKill] = useState('');
  const [status, setStatus] = useState('');
  const [busy, setBusy] = useState(false);

  const refresh = () => {
    setBusy(true);
    setStatus('');
    ListServerSessions(connId, database)
      .then(r => {
        setRows(r);
        setSelected('');
        setPendingKill('');
        setError('');
      })
      .catch((e: Error) => setError(e.message))
      .finally(() => setBusy(false));
  };

  useEffect(() => {
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const kill = () => {
    if (!selected) {
      setStatus('先在列表里选中一个会话');
      return;
    }
    // 两步确认：第一次进入确认态，第二次同一选中才执行。
    if (pendingKill !== selected) {
      setPendingKill(selected);
      setStatus(`再按一次确认终止 ${selected}（未提交事务会回滚）`);
      return;
    }
    setPendingKill('');
    setBusy(true);
    KillServerSession(connId, database, selected)
      .then(() => {
        setStatus(`已终止 ${selected}`);
        refresh();
      })
      .catch((e: Error) => {
        setStatus(`终止失败：${e.message}`);
        setBusy(false);
      });
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40" onClick={onClose}>
      <div
        ref={panelRef}
        className="bg-surface border border-border rounded-lg shadow-xl w-[760px] max-h-[560px] flex flex-col"
        onClick={e => e.stopPropagation()}
        role="dialog"
        aria-label="Server sessions"
      >
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border">
          <span className="font-semibold text-sm">Sessions</span>
          <span className="text-xs text-dim">其他会话（不含当前连接）</span>
          <div className="flex-1" />
          <button
            className="tool-btn"
            onClick={refresh}
            disabled={busy}
            title="刷新"
          >
            <FiRefreshCw className={busy ? 'animate-spin' : ''} /> 刷新
          </button>
          <button
            className={`tool-btn ${pendingKill === selected && selected ? 'text-danger border-danger' : ''}`}
            onClick={kill}
            disabled={busy || !selected}
            title="终止选中的会话（两步确认）"
          >
            <FiAlertTriangle /> {pendingKill === selected && selected ? `确认终止 ${selected}？` : '终止选中'}
          </button>
          <button className="tool-btn" onClick={onClose} title="关闭">
            <FiX /> 关闭
          </button>
        </div>
        {error ? (
          <div className="px-4 py-3 text-sm text-dim">{error}</div>
        ) : (
          <>
            {status && <div className="px-4 py-1 text-xs text-dim">{status}</div>}
            <div className="flex-1 overflow-auto px-4 py-2">
              {rows && rows.length === 0 && (
                <div className="text-sm text-dim py-4">服务器上没有其它活动会话。</div>
              )}
              {rows && rows.length > 0 && (
                <table className="w-full text-xs">
                  <thead>
                    <tr className="text-left text-dim border-b border-border">
                      <th className="py-1 pr-2">ID</th>
                      <th className="py-1 pr-2">用户</th>
                      <th className="py-1 pr-2">客户端</th>
                      <th className="py-1 pr-2">库</th>
                      <th className="py-1 pr-2">状态</th>
                      <th className="py-1 pr-2">时长</th>
                      <th className="py-1">语句</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map(r => (
                      <tr
                        key={r.id}
                        onClick={() => setSelected(r.id)}
                        className={`cursor-pointer border-b border-border/50 ${
                          selected === r.id ? 'bg-selected' : ''
                        } hover:bg-hover`}
                      >
                        <td className="py-1 pr-2 font-mono">{r.id}</td>
                        <td className="py-1 pr-2">{r.user}</td>
                        <td className="py-1 pr-2">{r.client}</td>
                        <td className="py-1 pr-2">{r.db}</td>
                        <td className="py-1 pr-2">{r.state || r.command}</td>
                        <td className="py-1 pr-2">{r.time}</td>
                        <td className="py-1 truncate max-w-[220px]" title={r.query}>
                          {r.query}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
};

export default SessionsDialog;
