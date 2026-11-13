import React, { useState } from 'react';
import { FiBox, FiPlay, FiRefreshCw, FiTerminal, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  CloseContainerSession,
  ContainerExec,
  ContainerExecClose,
  ContainerExecData,
  ContainerRow,
  KubectlRun,
  ListContainers,
  OpenContainerSession,
} from '../lib/workbench';

// W-C1: the containers panel — list containers through the ssh tunnel and run
// kubectl on the remote host.
//
// What this panel demonstrates, and what it deliberately is not:
//   - the session opens THROUGH tunnel.proxy: the host resolves the consumer's
//     declared dependency (navop-ssh's chain coordinates) and the plugin dials
//     the docker daemon inside that chain. Without navop-ssh, the consumer's
//     fiber is Inactive and "open" refuses with the reason — the panel prints it
//     verbatim instead of pretending the feature is missing;
//   - exec reuses the terminal channel contract (start shell → poll data →
//     close), rendered as a plain output pane: a full container terminal is a
//     UI concern that rides the same calls;
//   - kubectl is a passthrough to the REMOTE host, so the results are whatever
//     that host's kubectl says — the panel does not parse or summarize them.

interface ContainersPanelProps {
  onClose: () => void;
}

const ContainersPanel: React.FC<ContainersPanelProps> = ({ onClose }) => {
  const [session, setSession] = useState('');
  const [rows, setRows] = useState<ContainerRow[]>([]);
  const [form, setForm] = useState({ host: '127.0.0.1', port: 22, user: '', password: '', dockerAddr: '' });
  const [kubectlArgs, setKubectlArgs] = useState('get pods -A');
  const [kubectlOut, setKubectlOut] = useState('');
  const [shellOut, setShellOut] = useState('');
  const [shellId, setShellId] = useState('');
  const [busy, setBusy] = useState(false);

  const open = async () => {
    setBusy(true);
    try {
      const res = await OpenContainerSession({
        host: form.host,
        port: form.port,
        user: form.user,
        password: form.password,
        dockerAddr: form.dockerAddr || undefined,
      });
      setSession(res.sessionId);
      const list = await ListContainers(res.sessionId);
      setRows(list.containers ?? []);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const refresh = async () => {
    if (!session) return;
    try {
      const list = await ListContainers(session);
      setRows(list.containers ?? []);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const close = async () => {
    if (!session) return;
    try {
      await CloseContainerSession(session);
    } catch (e) {
      toast.error((e as Error).message);
    }
    setSession('');
    setRows([]);
    setShellOut('');
    setShellId('');
  };

  const runKubectl = async () => {
    if (!session) return;
    setBusy(true);
    try {
      const res = await KubectlRun(session, kubectlArgs);
      setKubectlOut(res.output ?? '');
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const openShell = async (container: ContainerRow) => {
    setBusy(true);
    try {
      const res = await ContainerExec(session, container.name);
      setShellId(res.channelId);
      // One drain right away: many shells print a prompt immediately.
      const first = await ContainerExecData(res.channelId, '');
      setShellOut(first.output ?? '');
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const sendShell = async (cmd: string) => {
    if (!shellId) return;
    try {
      const res = await ContainerExecData(shellId, `${cmd}\n`);
      setShellOut(prev => prev + (res.output ?? ''));
      if (res.ended) {
        setShellId('');
      }
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const closeShell = async () => {
    if (!shellId) return;
    try {
      await ContainerExecClose(shellId);
    } catch (e) {
      toast.error((e as Error).message);
    }
    setShellId('');
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="容器工作台"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-4xl h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-3 py-2 border-b border-line">
          <FiBox size={14} className="text-accent shrink-0" />
          <span className="text-sm font-medium text-ink flex-1">
            容器 {session && <span className="text-2xs text-muted">（会话 {session}）</span>}
          </span>
          <button className="p-1 text-secondary hover:text-ink" onClick={() => void refresh()} aria-label="刷新">
            <FiRefreshCw size={13} />
          </button>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        {!session ? (
          <div className="p-6 space-y-3">
            <div className="grid grid-cols-2 gap-2 text-2xs">
              <input className="bg-canvas border border-line-strong rounded px-2 py-1.5 text-ink" placeholder="SSH 主机"
                value={form.host} onChange={e => setForm(f => ({ ...f, host: e.target.value }))} aria-label="SSH 主机" />
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
              placeholder="docker 守护进程地址（默认 127.0.0.1:2375，在 SSH 链内拨号）"
              value={form.dockerAddr} onChange={e => setForm(f => ({ ...f, dockerAddr: e.target.value }))}
              aria-label="docker 地址" />
            <button
              className="w-full py-1.5 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              onClick={() => void open()}
              disabled={busy || form.host === ''}
            >
              {busy ? '连接中…' : '经 SSH 隧道打开容器会话'}
            </button>
            <p className="text-2xs text-muted leading-relaxed">
              容器能力依赖 navop-ssh 提供的 tunnel.proxy：没有 navop-ssh 时本插件纤维保持
              Inactive，打开会话会直接报出原因。docker 守护进程在 SSH 链内拨号，不需要在本机
              暴露 2375 端口。
            </p>
          </div>
        ) : (
          <>
            <div className="flex items-center gap-2 px-3 py-1.5 border-b border-line text-2xs">
              <input
                className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1 text-ink font-mono"
                value={kubectlArgs}
                onChange={e => setKubectlArgs(e.target.value)}
                aria-label="kubectl 参数"
              />
              <button
                className="flex items-center gap-1 px-2 py-1 rounded border border-line text-secondary hover:text-ink disabled:opacity-40"
                onClick={() => void runKubectl()}
                disabled={busy || kubectlArgs.trim() === ''}
              >
                <FiPlay size={10} /> kubectl
              </button>
              <button className="text-secondary hover:text-danger" onClick={() => void close()} aria-label="断开会话">
                <FiX size={12} />
              </button>
            </div>

            <div className="flex-1 min-h-0 overflow-auto">
              <table className="w-full text-2xs">
                <thead className="bg-raised text-muted uppercase tracking-wide sticky top-0">
                  <tr>
                    <th className="text-left px-2 py-1.5 font-medium">ID</th>
                    <th className="text-left px-2 py-1.5 font-medium">名称</th>
                    <th className="text-left px-2 py-1.5 font-medium">镜像</th>
                    <th className="text-left px-2 py-1.5 font-medium">状态</th>
                    <th className="text-right px-2 py-1.5 font-medium">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map(c => (
                    <tr key={c.id} className="border-t border-line hover:bg-raised">
                      <td className="px-2 py-1 font-mono text-muted">{c.id}</td>
                      <td className="px-2 py-1 text-ink">{c.name}</td>
                      <td className="px-2 py-1 text-muted">{c.image}</td>
                      <td className="px-2 py-1">
                        <span className={c.state === 'running' ? 'text-success' : 'text-muted'}>{c.state}</span>
                      </td>
                      <td className="px-2 py-1 text-right">
                        <button
                          className="flex items-center gap-1 text-secondary hover:text-accent ml-auto disabled:opacity-40"
                          onClick={() => void openShell(c)}
                          disabled={busy || c.state !== 'running'}
                          title="进入容器（exec）"
                        >
                          <FiTerminal size={11} /> exec
                        </button>
                      </td>
                    </tr>
                  ))}
                  {rows.length === 0 && (
                    <tr>
                      <td colSpan={5} className="px-2 py-3 text-muted">
                        没有容器（或守护进程尚未返回列表）。
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>

            {(kubectlOut || shellId || shellOut) && (
              <div className="border-t border-line max-h-56 overflow-auto">
                {kubectlOut && (
                  <pre className="px-3 py-2 text-2xs font-mono text-ink whitespace-pre-wrap">{kubectlOut}</pre>
                )}
                {shellId && (
                  <div className="px-3 py-2 space-y-1">
                    <div className="flex items-center gap-2 text-2xs">
                      <span className="text-muted">exec {shellId}</span>
                      <ShellInput onSend={cmd => void sendShell(cmd)} />
                      <button className="text-secondary hover:text-danger" onClick={() => void closeShell()}>
                        结束
                      </button>
                    </div>
                    <pre className="text-2xs font-mono text-ink whitespace-pre-wrap max-h-32 overflow-auto">{shellOut}</pre>
                  </div>
                )}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
};

// ShellInput keeps the one-line command box local so typing does not re-render
// the container table.
const ShellInput: React.FC<{ onSend: (cmd: string) => void }> = ({ onSend }) => {
  const [value, setValue] = React.useState('');
  return (
    <input
      className="flex-1 bg-canvas border border-line-strong rounded px-2 py-0.5 text-ink font-mono"
      value={value}
      placeholder="输入命令，回车发送"
      onChange={e => setValue(e.target.value)}
      onKeyDown={e => {
        if (e.key === 'Enter' && value.trim() !== '') {
          onSend(value);
          setValue('');
        }
      }}
      aria-label="容器命令"
    />
  );
};

export default ContainersPanel;
