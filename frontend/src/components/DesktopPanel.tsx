import React, { useCallback, useEffect, useState } from 'react';
import { FiMonitor, FiPlay, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  CloseDesktopSession,
  DesktopRow,
  DesktopState,
  OpenVnc,
  StartRdp,
  VncRelay,
} from '../lib/workbench';

// W-D1: the remote-desktop panel. Two transports with DIFFERENT semantics, and the
// panel says so instead of hiding it:
//
//   RDP — an EMISSION. Starting it spawns the client and there is NO undo; the
//         only compensation is the .rdp temp file, which the host deletes when the
//         session is closed or the plugin unloads. The panel shows the file path
//         and the "不可撤回" notice verbatim.
//   VNC — an ACQUISITION. The frames ride the channel; the panel can send a probe
//         (RFB greeting) and show the bytes that came back, which is what proves
//         the relay. A full embedded viewer (noVNC) is the remaining UX work: the
//         pipe it needs is the one this panel is driving.

const DesktopPanel: React.FC<{ onClose: () => void }> = ({ onClose }) => {
  const [rows, setRows] = useState<DesktopRow[]>([]);
  const [form, setForm] = useState({ host: '127.0.0.1', port: 3389, user: '', vncPort: 5900 });
  const [rdpInfo, setRdpInfo] = useState<{ file: string; target: string; notice: string } | null>(null);
  const [vnc, setVnc] = useState<{ session: string; target: string } | null>(null);
  const [probe, setProbe] = useState('RFB 003.008\n');
  const [reply, setReply] = useState('');
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const res = await DesktopState();
      setRows(res.sessions ?? []);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const startRdp = async () => {
    setBusy(true);
    try {
      const res = await StartRdp(form.host, form.port, form.user || undefined);
      setRdpInfo({ file: res.file, target: res.target, notice: res.notice });
      toast.success(`已拉起 RDP：${res.target}（不可撤回）`);
      await refresh();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const startVnc = async () => {
    setBusy(true);
    try {
      const res = await OpenVnc(form.host, form.vncPort);
      setVnc(res);
      await refresh();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const sendProbe = async () => {
    if (!vnc) return;
    try {
      // The browser has btoa; the JBIG/RFB greeting is ASCII, which is enough for
      // the probe. A real viewer would stream binary frames the same way.
      const b64 = btoa(probe);
      const res = await VncRelay(vnc.session, b64);
      if (res.bytes === 0) {
        setReply('（暂无回帧：对端尚未发送）');
        return;
      }
      setReply(atob(res.data));
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const closeSession = async (tag: string) => {
    try {
      await CloseDesktopSession(tag);
      if (vnc?.session === tag) setVnc(null);
      if (rdpInfo) setRdpInfo(null);
      await refresh();
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="远程桌面"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-3xl max-h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-3 py-2 border-b border-line">
          <FiMonitor size={14} className="text-accent shrink-0" />
          <span className="text-sm font-medium text-ink flex-1">远程桌面</span>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        <div className="p-3 space-y-3">
          <div className="grid grid-cols-2 gap-2 text-2xs">
            <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="主机"
              value={form.host} onChange={e => setForm(f => ({ ...f, host: e.target.value }))} aria-label="主机" />
            <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="用户（可空）"
              value={form.user} onChange={e => setForm(f => ({ ...f, user: e.target.value }))} aria-label="用户" />
            <div className="flex items-center gap-1">
              <span className="text-muted w-8">RDP</span>
              <input className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" type="number"
                value={form.port} onChange={e => setForm(f => ({ ...f, port: Number(e.target.value) }))} aria-label="RDP 端口" />
            </div>
            <div className="flex items-center gap-1">
              <span className="text-muted w-8">VNC</span>
              <input className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" type="number"
                value={form.vncPort} onChange={e => setForm(f => ({ ...f, vncPort: Number(e.target.value) }))} aria-label="VNC 端口" />
            </div>
          </div>

          <div className="flex gap-2 text-2xs">
            <button
              className="flex-1 flex items-center justify-center gap-1 py-1.5 rounded bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              onClick={() => void startRdp()}
              disabled={busy || form.host === ''}
            >
              <FiPlay size={10} /> 拉起 RDP（发射，不可撤回）
            </button>
            <button
              className="flex-1 flex items-center justify-center gap-1 py-1.5 rounded border border-line text-secondary hover:text-ink disabled:opacity-40"
              onClick={() => void startVnc()}
              disabled={busy || form.host === ''}
            >
              打开 VNC 通道
            </button>
          </div>

          {rdpInfo && (
            <div className="text-2xs text-warning border border-warning/30 rounded p-2 space-y-1" role="alert">
              <div>{rdpInfo.notice}</div>
              <div className="font-mono break-all text-muted">.rdp: {rdpInfo.file}</div>
            </div>
          )}

          {vnc && (
            <div className="border border-line rounded p-2 space-y-2 text-2xs">
              <div className="flex items-center gap-2">
                <span className="text-muted">VNC {vnc.target}</span>
                <span className="flex-1" />
                <button className="text-secondary hover:text-danger" onClick={() => void closeSession(vnc.session)}>
                  关闭通道
                </button>
              </div>
              <div className="flex items-center gap-2">
                <input className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1 text-ink font-mono"
                  value={probe} onChange={e => setProbe(e.target.value)} aria-label="VNC 探针" />
                <button className="px-2 py-1 rounded border border-line text-secondary hover:text-ink"
                  onClick={() => void sendProbe()}>发送</button>
              </div>
              {reply && <pre className="text-ink font-mono whitespace-pre-wrap max-h-24 overflow-auto">{reply}</pre>}
            </div>
          )}

          {rows.length > 0 && (
            <div className="text-2xs">
              <div className="text-muted mb-1">活动会话：</div>
              {rows.map(r => (
                <div key={r.tag} className="flex items-center gap-2 border-t border-line py-1">
                  <span className="text-accent">{r.kind ?? '?'}</span>
                  <span className="text-ink font-mono">{r.target ?? r.tag}</span>
                  <span className="flex-1" />
                  <button className="text-secondary hover:text-danger" onClick={() => void closeSession(r.tag)}>
                    关闭
                  </button>
                </div>
              ))}
            </div>
          )}

          <p className="text-2xs text-muted leading-relaxed">
            RDP 的拉起是不可撤回的发射：宿主只为它写入的 .rdp 临时文件登记清理逆（关闭会话或卸载插件时删除）。
            VNC 走 channel：帧以 base64 经请求/响应搬运，内嵌完整查看器（noVNC）是后续 UI 工作。
          </p>
        </div>
      </div>
    </div>
  );
};

export default DesktopPanel;
