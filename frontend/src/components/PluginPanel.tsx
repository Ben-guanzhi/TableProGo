import React, { useEffect, useRef, useState } from 'react';
import { FiAlertTriangle, FiCheck, FiFolder, FiRefreshCw, FiShield, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  EnsurePluginRegistry,
  ListPlugins,
  PluginInfo,
  PluginPanelData,
  TrustPlugin,
  UntrustPlugin,
} from '../api';
import { useDialog } from '../dialog';

interface PluginPanelProps {
  onClose: () => void;
}

// The three states, and why they are not symmetric:
//
//   trusted  digest matches what was vouched for      → the gate runs it
//   unknown  nobody vouched for this file             → the gate runs it, and says so
//   tampered a vouched path whose bytes changed       → the gate REFUSES it
//
// The panel has to show all three because the middle one is the dev workflow and
// the last one is the attack. Collapsing them into "on/off" would hide both.
const stateStyle = (state: PluginInfo['state']) => {
  switch (state) {
    case 'trusted':
      return { label: '已信任', className: 'text-success', icon: <FiCheck size={11} /> };
    case 'tampered':
      return { label: '已被替换', className: 'text-danger', icon: <FiAlertTriangle size={11} /> };
    default:
      return { label: '未信任', className: 'text-warning', icon: <FiShield size={11} /> };
  }
};

// W10: 插件面板.
//
// The reason this exists at all: the plugin model runs an executable the user did
// not build, so the one thing the UI must make impossible is a silent substitution.
// A tampered row is loud, its digests are shown, and the trust button re-records
// the CURRENT bytes only when the user says so — never automatically.
const PluginPanel: React.FC<PluginPanelProps> = ({ onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [data, setData] = useState<PluginPanelData | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState<string>('');

  const refresh = async () => {
    setLoading(true);
    setError('');
    try {
      setData(await ListPlugins());
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void refresh();
    // Once on open: the panel is re-read after every mutation instead.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const trust = async (p: PluginInfo) => {
    setBusy(p.path);
    try {
      await TrustPlugin(p.path, p.type);
      toast.success(`已信任 ${p.name}`);
      await refresh();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  const untrust = async (p: PluginInfo) => {
    setBusy(p.path);
    try {
      await UntrustPlugin(p.path);
      toast.success(`已移出信任清单：${p.name}`);
      await refresh();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  const makeRegistry = async () => {
    try {
      const res = await EnsurePluginRegistry();
      toast.success(`已创建注册表：${res.path}`);
      await refresh();
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const plugins = data?.plugins ?? [];
  const warnings = data?.warnings ?? [];

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="plugin-panel-title"
        className="bg-panel rounded-lg w-full max-w-3xl max-h-[88vh] flex flex-col shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center px-5 py-3 border-b border-line shrink-0">
          <FiShield size={15} className="mr-2 text-accent" />
          <h2 id="plugin-panel-title" className="text-sm font-semibold">
            插件
          </h2>
          <button
            className="ml-auto mr-1 p-1 text-secondary hover:text-ink"
            onClick={() => void refresh()}
            aria-label="刷新插件列表"
            title="刷新"
          >
            <FiRefreshCw size={13} className={loading ? 'animate-spin' : ''} />
          </button>
          <button className="p-1 text-secondary hover:text-ink" onClick={onClose} aria-label="关闭插件面板">
            <FiX size={16} />
          </button>
        </div>

        <div className="flex-1 overflow-auto p-5 space-y-4">
          <p className="text-2xs text-muted leading-relaxed">
            插件是独立的可执行文件，本程序会用子进程方式启动它。启动前会先算文件的 SHA-256：
            <span className="text-success">已信任</span> 且摘要一致的才会运行；
            <span className="text-warning">未信任</span> 的会运行但在此处标出；
            <span className="text-danger">摘要不符</span> 的一律拒绝——那是「你信任过的文件被换掉了」。
          </p>

          {error && (
            <p className="text-xs text-danger break-words" role="alert">
              {error}
            </p>
          )}

          {warnings.map((w, i) => (
            <p key={i} className="text-2xs text-warning flex items-start gap-1">
              <FiAlertTriangle size={11} className="mt-0.5 shrink-0" />
              <span className="break-words">{w}</span>
            </p>
          ))}

          {plugins.length === 0 && !loading && (
            <div className="text-center py-8 space-y-2">
              <p className="text-xs text-muted">还没有声明任何插件。</p>
              <button
                className="px-3 py-1.5 rounded text-xs bg-raised hover:bg-emphasis text-ink border border-line"
                onClick={() => void makeRegistry()}
              >
                创建示例注册表
              </button>
            </div>
          )}

          {plugins.length > 0 && (
            <div className="border border-line rounded overflow-hidden">
              <table className="w-full text-xs">
                <thead className="bg-raised text-2xs uppercase tracking-wide text-muted">
                  <tr>
                    <th className="text-left px-2 py-1.5 font-medium">插件</th>
                    <th className="text-left px-2 py-1.5 font-medium">来源</th>
                    <th className="text-left px-2 py-1.5 font-medium">状态</th>
                    <th className="text-right px-2 py-1.5 font-medium">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {plugins.map(p => {
                    const style = stateStyle(p.state);
                    return (
                      <tr key={`${p.type}-${p.source}`} className="border-t border-line align-top">
                        <td className="px-2 py-1.5">
                          <div className="text-ink">{p.name}</div>
                          <div className="text-2xs text-muted font-mono break-all">{p.path || '（未给出路径）'}</div>
                          {p.description && (
                            <div className="text-2xs text-muted mt-0.5">{p.description}</div>
                          )}
                        </td>
                        <td className="px-2 py-1.5 text-2xs text-secondary">
                          {p.source === 'env' ? '环境变量' : '注册表'}
                          {!p.exists && <div className="text-warning">文件不存在</div>}
                        </td>
                        <td className="px-2 py-1.5">
                          <span className={`flex items-center gap-1 ${style.className}`}>
                            {style.icon}
                            {style.label}
                          </span>
                          <div className="text-2xs text-muted break-words max-w-[220px]">{p.detail}</div>
                          {p.state === 'tampered' && p.expected && (
                            <div className="text-2xs text-danger font-mono break-all mt-0.5">
                              期望 {p.expected.slice(0, 16)}… / 实际 {(p.actual ?? '').slice(0, 16)}…
                            </div>
                          )}
                        </td>
                        <td className="px-2 py-1.5 text-right whitespace-nowrap">
                          {p.state !== 'trusted' && p.exists && (
                            <button
                              className="px-2 py-0.5 rounded text-2xs border border-line text-secondary hover:text-ink hover:bg-raised disabled:opacity-40"
                              onClick={() => void trust(p)}
                              disabled={busy === p.path}
                              title="记录当前文件的摘要，下次启动即按它校验"
                            >
                              信任当前文件
                            </button>
                          )}
                          {p.state === 'trusted' && (
                            <button
                              className="px-2 py-0.5 rounded text-2xs border border-line text-secondary hover:text-danger hover:bg-raised disabled:opacity-40"
                              onClick={() => void untrust(p)}
                              disabled={busy === p.path}
                              title="移出信任清单；下次启动会把它当作未信任（仍可运行，会提示）"
                            >
                              移出信任
                            </button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}

          {data && (
            <div className="text-2xs text-muted space-y-0.5 border-t border-line pt-2">
              <p className="flex items-center gap-1">
                <FiFolder size={10} />
                注册表：<span className="font-mono break-all">{data.registryPath}</span>
              </p>
              <p className="flex items-center gap-1">
                <FiFolder size={10} />
                信任清单：<span className="font-mono break-all">{data.trustPath}</span>
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

export default PluginPanel;
