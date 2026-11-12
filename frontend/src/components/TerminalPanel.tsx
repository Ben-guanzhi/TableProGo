import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { FiAlertTriangle, FiPlus, FiRefreshCw, FiSquare, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  BroadcastConfirmed,
  BroadcastWorkbench,
  CloseWorkbenchChannel,
  GetWorkbenchState,
  OpenWorkbenchChannel,
  WriteWorkbenchChannel,
  WorkbenchState,
} from '../lib/workbench';
import {
  PaneTree,
  closePaneRoot,
  countLeaves,
  findLeafByChannel,
  flatten,
  newLeaf,
  setLeafChannel,
  splitPane,
} from '../lib/termLayout';

// W-T2: the terminal panel — 多标签 + 分屏 + 广播.
//
// Three things and their sources:
//   tabs      the nav.nodes bindings (the plugin's declared entry points), read
//             from /api/workbench/state — the plugin owns the tab list, the host
//             only renders it;
//   panes     the termLayout tree — user intent (split/close), pure functions;
//   channels  the terminal.channels bindings + per-pane output buffers — runtime
//             state, polled from /api/workbench/channel data.
//
// The BROADCAST is the emission case (AD-3): typing into the broadcast box sends
// the SAME input to every selected channel, which cannot be undone. When
// safeModeOn is true the first post is WITHHELD and the panel shows the warning
// bar with a confirm button; the confirmed route is a separate endpoint, so the
// flag cannot bypass the prompt. When Safe Mode is off the broadcast runs
// immediately and the bar still says so — the UI never pretends an undo exists.
//
// Rendering honesty (recorded, not hidden): W-T2 renders the output as plain
// text in a scrolling pre — no xterm.js yet, no ANSI colors, no resize-driven
// reflow beyond fixed 80×24. The MECHANISM (layout, broadcast, multi-tab) is
// this wave; the fidelity upgrade is a later polish, and it cannot be blocked by
// anything here because the panel only owns rendering.

interface TerminalPanelProps {
  onClose: () => void;
}

// PaneView is one leaf's runtime state: the accumulated output and the input box.
interface PaneView {
  output: string;
  ended: boolean;
}

const POLL_MS = 600;

const TerminalPanel: React.FC<TerminalPanelProps> = ({ onClose }) => {
  const [state, setState] = useState<WorkbenchState | null>(null);
  const [tree, setTree] = useState<PaneTree>(() => newLeaf('p1'));
  // Per-leaf view state keyed by LEAF id: output accumulates per pane, and a
  // pane's channel may be rebound without losing what was on screen.
  const [views, setViews] = useState<Record<string, PaneView>>({});
  const [focusedLeaf, setFocusedLeaf] = useState('p1');
  const [inputs, setInputs] = useState<Record<string, string>>({});
  const [broadcastSel, setBroadcastSel] = useState<Record<string, boolean>>({});
  const [broadcastInput, setBroadcastInput] = useState('');
  const [connectForm, setConnectForm] = useState({ host: '127.0.0.1', port: 22, user: '', password: '' });
  const [busy, setBusy] = useState(false);
  const [confirmBar, setConfirmBar] = useState<{ ids: string[]; input: string } | null>(null);
  const leafSeq = useRef(1);

  const refreshState = useCallback(async () => {
    try {
      setState(await GetWorkbenchState());
    } catch (e) {
      toast.error((e as Error).message);
    }
  }, []);

  useEffect(() => {
    void refreshState();
  }, [refreshState]);

  // The poller: drain every connected pane's output. 600ms is interactive
  // pacing; the per-channel buffer on the plugin side absorbs the gap.
  useEffect(() => {
    const timer = setInterval(async () => {
      const leaves = flatten(tree).filter(l => l.channelId);
      for (const l of leaves) {
        if (!l.channelId) continue;
        try {
          const res = await WriteWorkbenchChannel(l.channelId, '', 16384);
          if (res.output) {
            setViews(v => ({
              ...v,
              [l.id]: { output: (v[l.id]?.output ?? '') + res.output, ended: res.ended },
            }));
          } else if (res.ended) {
            setViews(v => ({ ...v, [l.id]: { ...(v[l.id] ?? { output: '' }), ended: true } }));
          }
        } catch {
          // A dead plugin process surfaces through the next state refresh; the
          // poller must not toast on every tick while the fiber is down.
        }
      }
    }, POLL_MS);
    return () => clearInterval(timer);
  }, [tree]);

  const openChannel = async (leafId: string) => {
    setBusy(true);
    try {
      const res = await OpenWorkbenchChannel({
        host: connectForm.host,
        port: connectForm.port,
        user: connectForm.user,
        password: connectForm.password,
        cols: 80,
        rows: 24,
      });
      setTree(t => setLeafChannel(t, leafId, res.channelId));
      setViews(v => ({ ...v, [leafId]: { output: '', ended: false } }));
      setFocusedLeaf(leafId);
      toast.success('终端已连接');
      void refreshState();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const closeChannel = async (leafId: string) => {
    const leaf = findLeafById(tree, leafId);
    if (!leaf?.channelId) return;
    try {
      await CloseWorkbenchChannel(leaf.channelId);
      setTree(t => setLeafChannel(t, leafId, null));
      void refreshState();
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const sendInput = async (leafId: string, input: string) => {
    const leaf = findLeafById(tree, leafId);
    if (!leaf?.channelId) return;
    setInputs(v => ({ ...v, [leafId]: '' }));
    try {
      const res = await WriteWorkbenchChannel(leaf.channelId, input + '\n', 16384);
      setViews(v => ({
        ...v,
        [leafId]: { output: (v[leafId]?.output ?? '') + res.output, ended: res.ended },
      }));
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  // Broadcast targets: the TICKED panes, or the focused pane when none ticked.
  const broadcastTargets = useMemo(() => {
    const leaves = flatten(tree).filter(l => l.channelId);
    const chosen = leaves.filter(l => l.id !== focusedLeaf && broadcastSel[l.id]);
    const focused = leaves.find(l => l.id === focusedLeaf);
    const ids = chosen.length > 0 ? chosen.map(l => l.channelId!) : focused?.channelId ? [focused.channelId] : [];
    return ids.filter((v, i, a) => a.indexOf(v) === i);
  }, [tree, focusedLeaf, broadcastSel]);

  const doBroadcast = async (confirmed: boolean) => {
    if (broadcastTargets.length === 0 || broadcastInput === '') return;
    setBusy(true);
    try {
      const call = confirmed ? BroadcastConfirmed : BroadcastWorkbench;
      const res = await call(broadcastTargets, broadcastInput + '\n');
      if (res.withheld) {
        // The host withheld: show the warning bar with the exact ids/input so the
        // confirm re-posts the SAME emission.
        setConfirmBar({ ids: res.channelIds ?? [], input: res.input ?? '' });
        return;
      }
      // Distribute the per-channel outputs into their panes.
      setViews(v => {
        const next = { ...v };
        for (const [cid, out] of Object.entries(res.results ?? {})) {
          const leaf = findLeafByChannel(tree, cid);
          if (leaf) {
            next[leaf.id] = { output: (next[leaf.id]?.output ?? '') + out, ended: next[leaf.id]?.ended ?? false };
          }
        }
        return next;
      });
      setConfirmBar(null);
      setBroadcastInput('');
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const splitFocused = (dir: 'h' | 'v') => {
    leafSeq.current += 1;
    const newId = `p${leafSeq.current}`;
    setTree(t => splitPane(t, focusedLeaf, dir, newId));
    setFocusedLeaf(newId);
  };

  const closeFocused = () => {
    leafSeq.current += 1;
    const rebornId = `p${leafSeq.current}`;
    setTree(t => closePaneRoot(t, focusedLeaf));
    // The closed leaf's view dies with it; the reborn leaf starts clean.
    setViews(v => ({ ...v, [rebornId]: { output: '', ended: false } }));
    setFocusedLeaf(rebornId);
  };

  const leaves = flatten(tree);

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label="终端工作台"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-6xl h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        {/* header: tabs from nav.nodes + the state refresh */}
        <div className="flex items-center gap-2 px-3 py-2 border-b border-line">
          <FiSquare size={13} className="text-accent shrink-0" />
          <div className="flex items-center gap-1 overflow-x-auto flex-1">
            {(state?.navNodes ?? []).map(n => (
              <span key={n.tag} className="text-xs px-2 py-0.5 rounded bg-raised text-ink whitespace-nowrap">
                {n.label || n.tag}
              </span>
            ))}
            {(state?.navNodes?.length ?? 0) === 0 && (
              <span className="text-2xs text-muted">没有已装载的终端能力（安装 navop-ssh 插件后出现）</span>
            )}
          </div>
          <button
            className="p-1 text-secondary hover:text-ink"
            onClick={() => void refreshState()}
            title="刷新工作台状态"
            aria-label="刷新"
          >
            <FiRefreshCw size={13} />
          </button>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        {/* toolbar: split / close / new */}
        <div className="flex items-center gap-2 px-3 py-1.5 border-b border-line text-2xs text-secondary">
          <button className="hover:text-ink flex items-center gap-1" onClick={() => splitFocused('h')} disabled={busy}>
            <FiPlus size={11} /> 横向分屏
          </button>
          <button className="hover:text-ink flex items-center gap-1" onClick={() => splitFocused('v')} disabled={busy}>
            <FiPlus size={11} /> 纵向分屏
          </button>
          <button className="hover:text-ink flex items-center gap-1" onClick={closeFocused} disabled={countLeaves(tree) <= 1}>
            <FiX size={11} /> 关闭当前面板
          </button>
          <span className="flex-1" />
          {state?.safeModeOn && (
            <span className="flex items-center gap-1 text-warning">
              <FiAlertTriangle size={11} /> Safe Mode：广播需要确认
            </span>
          )}
        </div>

        {/* connect form: the one place credentials appear (AD-4 follow-up noted) */}
        <div className="flex items-end gap-2 px-3 py-2 border-b border-line text-2xs">
          <input
            className="bg-canvas border border-line-strong rounded px-2 py-1 text-ink w-40"
            placeholder="主机"
            value={connectForm.host}
            onChange={e => setConnectForm(f => ({ ...f, host: e.target.value }))}
            aria-label="SSH 主机"
          />
          <input
            className="bg-canvas border border-line-strong rounded px-2 py-1 text-ink w-16"
            placeholder="端口"
            type="number"
            value={connectForm.port}
            onChange={e => setConnectForm(f => ({ ...f, port: Number(e.target.value) }))}
            aria-label="SSH 端口"
          />
          <input
            className="bg-canvas border border-line-strong rounded px-2 py-1 text-ink w-24"
            placeholder="用户"
            value={connectForm.user}
            onChange={e => setConnectForm(f => ({ ...f, user: e.target.value }))}
            aria-label="SSH 用户"
          />
          <input
            className="bg-canvas border border-line-strong rounded px-2 py-1 text-ink w-32"
            placeholder="密码"
            type="password"
            value={connectForm.password}
            onChange={e => setConnectForm(f => ({ ...f, password: e.target.value }))}
            aria-label="SSH 密码"
          />
          <span className="text-muted">新面板连接时使用</span>
        </div>

        {/* panes */}
        <div className="flex-1 min-h-0 flex" style={{ flexDirection: tree.kind === 'split' && tree.dir === 'v' ? 'column' : 'row' }}>
          {leaves.map(leaf => {
            const view = views[leaf.id] ?? { output: '', ended: false };
            const focused = leaf.id === focusedLeaf;
            return (
              <div
                key={leaf.id}
                className={`flex-1 min-w-0 min-h-0 flex flex-col border-r border-line ${focused ? 'bg-canvas' : 'bg-panel'}`}
                onClick={() => setFocusedLeaf(leaf.id)}
              >
                <div className="flex items-center gap-2 px-2 py-1 border-b border-line text-2xs">
                  {leaf.channelId && (
                    <input
                      type="checkbox"
                      className="accent-accent shrink-0"
                      checked={broadcastSel[leaf.id] ?? false}
                      onChange={e => setBroadcastSel(v => ({ ...v, [leaf.id]: e.target.checked }))}
                      title="勾选加入广播"
                      aria-label="加入广播"
                    />
                  )}
                  <span className={focused ? 'text-ink' : 'text-muted'}>
                    {leaf.channelId ? `会话 ${leaf.channelId}` : '未连接'}
                  </span>
                  <span className="flex-1" />
                  {leaf.channelId ? (
                    <button
                      className="text-secondary hover:text-danger"
                      onClick={e => {
                        e.stopPropagation();
                        void closeChannel(leaf.id);
                      }}
                      title="断开此会话"
                      aria-label="断开会话"
                    >
                      <FiSquare size={10} />
                    </button>
                  ) : (
                    <button
                      className="text-secondary hover:text-accent flex items-center gap-1"
                      onClick={e => {
                        e.stopPropagation();
                        void openChannel(leaf.id);
                      }}
                      disabled={busy}
                      aria-label="连接终端"
                    >
                      <FiPlus size={10} /> 连接
                    </button>
                  )}
                </div>
                <pre className="flex-1 min-h-0 overflow-auto px-2 py-1 text-2xs font-mono text-ink whitespace-pre-wrap break-all m-0">
                  {view.output || (leaf.channelId ? '（等待输出…）' : '未连接。填写上方 SSH 信息后点「连接」。')}
                  {view.ended && '\n（会话已结束）'}
                </pre>
                {leaf.channelId && !view.ended && (
                  <form
                    className="flex items-center gap-1 px-2 py-1 border-t border-line"
                    onSubmit={e => {
                      e.preventDefault();
                      void sendInput(leaf.id, inputs[leaf.id] ?? '');
                    }}
                  >
                    <input
                      className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1 text-xs text-ink font-mono"
                      value={inputs[leaf.id] ?? ''}
                      onChange={e => setInputs(v => ({ ...v, [leaf.id]: e.target.value }))}
                      aria-label="终端输入"
                      placeholder={focused ? '输入命令…' : ''}
                    />
                  </form>
                )}
              </div>
            );
          })}
        </div>

        {/* broadcast: the emission gate */}
        <div className="border-t border-line px-3 py-2 space-y-1.5">
          {confirmBar && (
            <div className="flex items-center gap-2 text-2xs text-warning" role="alert">
              <FiAlertTriangle size={12} />
              <span>
                确认：将「{confirmBar.input.trim()}」发送到 {confirmBar.ids.length} 个终端会话？发送后不可撤回。
              </span>
              <button
                className="px-2 py-0.5 rounded bg-warning/20 text-warning hover:bg-warning/30"
                onClick={() => void doBroadcast(true)}
                disabled={busy}
              >
                确认发送
              </button>
              <button className="text-secondary hover:text-ink" onClick={() => setConfirmBar(null)}>
                取消
              </button>
            </div>
          )}
          <div className="flex items-center gap-2">
            <span className="text-2xs text-secondary whitespace-nowrap">
              广播 → {broadcastTargets.length} 个会话
            </span>
            <input
              className="flex-1 bg-canvas border border-line-strong rounded px-2 py-1 text-xs text-ink font-mono"
              value={broadcastInput}
              onChange={e => setBroadcastInput(e.target.value)}
              placeholder="同一命令发送到勾选的终端…"
              aria-label="广播输入"
            />
            <button
              className="px-2 py-1 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              onClick={() => void doBroadcast(false)}
              disabled={busy || broadcastTargets.length === 0 || broadcastInput === ''}
            >
              广播
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};

// findLeafById mirrors termLayout.findLeaf through the panel's local import.
function findLeafById(tree: PaneTree, leafId: string) {
  return flatten(tree).find(l => l.id === leafId) ?? null;
}

export default TerminalPanel;
