import React, { useCallback, useEffect, useState } from 'react';
import { FiActivity, FiPlus, FiRefreshCw, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import { post } from '../api';
import { CHART_HEIGHT, CHART_WIDTH, linePath, truncateLabel } from '../datagrid/chart';
import { latestValue, sparkPoints, sparkSummary, type MonitorState } from '../lib/monitor';

// W-M1: the monitor panel — one inline-SVG sparkline per sampled host.
//
// What the plan asks and where it landed:
//   ring       host.stats key (table semantics) — the Go side owns it;
//   15s timer  a tracked effect — unloading the plugin stops every sampler;
//   rendering  the HOST's UI — realized with the existing inline-SVG helpers
//              (chart.ts linePath), NOT a chart library: a sparkline does not
//              justify the largest dependency in the bundle for the smallest
//              feature.
//
// Honesty: the sampled VALUE is whatever the plugin submitted. The host-side
// samplerFunc is a placeholder that refuses (the panel shows "no sampler");
// real numbers come from a monitor plugin's submissions — the panel plots
// plugin-owned data, it does not invent platform counters.

interface MonitorPanelProps {
  onClose: () => void;
}

interface MonitorResponse {
  state?: MonitorState[];
}

const POLL_MS = 2000;

const MonitorPanel: React.FC<MonitorPanelProps> = ({ onClose }) => {
  const [states, setStates] = useState<MonitorState[]>([]);
  const [host, setHost] = useState('');
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const res = await post('/api/workbench/monitor', { action: 'state' }) as MonitorResponse;
      setStates(res.state ?? []);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(timer);
  }, [refresh]);

  const startMonitor = async () => {
    if (host === '') return;
    setBusy(true);
    try {
      await post('/api/workbench/monitor', { action: 'start', host, cap: 240 });
      setHost('');
      await refresh();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const stopMonitor = async (h: string) => {
    try {
      await post('/api/workbench/monitor', { action: 'stop', host: h });
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
        aria-label="监控工作台"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-3xl max-h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-3 py-2 border-b border-line">
          <FiActivity size={14} className="text-accent shrink-0" />
          <span className="text-sm font-medium text-ink flex-1">监控</span>
          <button className="p-1 text-secondary hover:text-ink" onClick={() => void refresh()} aria-label="刷新">
            <FiRefreshCw size={13} />
          </button>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        <div className="flex items-center gap-2 px-3 py-2 border-b border-line text-2xs">
          <input
            className="bg-canvas border border-line-strong rounded px-2 py-1 text-ink w-48"
            placeholder="主机名（如 web-1）"
            value={host}
            onChange={e => setHost(e.target.value)}
            aria-label="主机名"
          />
          <button
            className="flex items-center gap-1 px-2 py-1 rounded bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
            onClick={() => void startMonitor()}
            disabled={busy || host === ''}
          >
            <FiPlus size={11} /> 开始采样（15s）
          </button>
          <span className="text-muted">定时器是效应：插件卸载即停</span>
        </div>

        <div className="flex-1 min-h-0 overflow-auto p-3 space-y-3">
          {states.length === 0 && (
            <div className="text-2xs text-muted">
              没有采样环。输入主机名开始采样；采样值由监控插件提交，宿主只存储与回放。
            </div>
          )}
          {states.map(s => {
            const points = sparkPoints(s);
            const path = points.length >= 2 ? linePath({
              points,
              valueColumn: 'value',
              labelColumn: 'sample',
              kind: 'line',
            }) : null;
            return (
              <div key={s.tag} className="border border-line rounded p-2">
                <div className="flex items-center gap-2 text-2xs">
                  <span className="text-ink font-medium">{truncateLabel(s.host, 24)}</span>
                  <span className="text-muted">{sparkSummary(s)}</span>
                  <span className="flex-1" />
                  <button className="text-secondary hover:text-danger" onClick={() => void stopMonitor(s.host)}
                    aria-label={`停止 ${s.host} 的采样`}>
                    停止
                  </button>
                </div>
                <svg viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`} className="w-full h-28 mt-1" role="img"
                  aria-label={`${s.host} 采样环折线图`}>
                  {path ? (
                    <path d={path} fill="none" stroke="var(--color-accent, #4f7cff)" strokeWidth="1.5" />
                  ) : (
                    <text x={CHART_WIDTH / 2} y={CHART_HEIGHT / 2} textAnchor="middle"
                      className="fill-muted" fontSize="11">
                      {latestValue(s) === null ? '等待采样…' : '样本不足（≥2）'}
                    </text>
                  )}
                </svg>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
};

export default MonitorPanel;
