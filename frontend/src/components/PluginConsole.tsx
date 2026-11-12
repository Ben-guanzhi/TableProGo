import React, { useEffect, useMemo, useRef, useState } from 'react';
import { FiAlertTriangle, FiPlay, FiTerminal, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  GetPluginActions,
  PluginAction,
  PluginActionParam,
  RunPluginAction,
} from '../api';
import { QueryResult } from '../types';
import { useDialog } from '../dialog';
import { argsOf, cellText, initialValues, missingRequired } from '../lib/pluginConsole';

// W17: 插件控制台.
//
// This component knows NOTHING about vectors, topics, subjects or configs. It asks the
// connection's plugin what actions it offers, draws a field for each declared parameter,
// runs the one picked, and shows the result in a table. That ignorance is the feature:
// Milvus/Kafka/Pulsar/Nacos consoles ship as plugins, so adding an engine is a plugin —
// not a change here.
//
// A `danger` action changes remote state (publish a config, produce a message), so it is
// confirmed first. The plugin decides that flag; the host only honours it.

interface PluginConsoleProps {
  connId: string;
  connName: string;
  onClose: () => void;
}

// paramsOf returns the params to draw, with any declared default pre-filled.

const PluginConsole: React.FC<PluginConsoleProps> = ({ connId, connName, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [actions, setActions] = useState<PluginAction[]>([]);
  const [selectedID, setSelectedID] = useState('');
  const [values, setValues] = useState<Record<string, string>>({});
  const [result, setResult] = useState<QueryResult | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);

  const selected = useMemo(
    () => actions.find(a => a.id === selectedID) ?? null,
    [actions, selectedID],
  );
  const params = selected?.params ?? [];

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    GetPluginActions(connId)
      .then(list => {
        if (cancelled) return;
        setActions(list ?? []);
        if (list && list.length > 0) {
          setSelectedID(list[0].id);
          setValues(initialValues(list[0].params ?? []));
        }
      })
      .catch((e: Error) => {
        if (!cancelled) setError(e.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [connId]);

  const pick = (a: PluginAction) => {
    setSelectedID(a.id);
    setValues(initialValues(a.params ?? []));
    setResult(null);
    setError('');
  };

  const run = async () => {
    if (!selected) return;
    // A required field that is empty would send an action the plugin can only reject;
    // say which one is missing instead of relaying a server error.
    const missing = missingRequired(params, values);
    if (missing) {
      setError(`请填写「${missing}」`);
      return;
    }
    if (selected.danger && !window.confirm(`「${selected.title}」会改变远端状态，继续？`)) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const res = await RunPluginAction(connId, selected.id, argsOf(params, values));
      if (res?.error) {
        setError(res.error);
        setResult(null);
      } else {
        setResult(res);
      }
    } catch (e) {
      setError((e as Error).message);
      setResult(null);
    } finally {
      setBusy(false);
    }
  };

  // Group the actions so a plugin with several consoles (检索 / 消息 / 配置) reads as
  // sections rather than one flat list.
  const groups = useMemo(() => {
    const map = new Map<string, PluginAction[]>();
    for (const a of actions) {
      const key = a.group || '动作';
      if (!map.has(key)) map.set(key, []);
      map.get(key)!.push(a);
    }
    return [...map.entries()];
  }, [actions]);

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="插件控制台"
        className="bg-panel border border-line rounded-lg shadow-xl w-full max-w-4xl max-h-[85vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-line">
          <div className="flex items-center gap-2 min-w-0">
            <FiTerminal size={14} className="text-accent shrink-0" />
            <span className="text-sm font-medium text-ink truncate">插件控制台 · {connName}</span>
          </div>
          <button onClick={onClose} className="p-1 text-secondary hover:text-ink" aria-label="关闭">
            <FiX size={14} />
          </button>
        </div>

        <div className="flex-1 min-h-0 flex">
          {/* Action list */}
          <div className="w-52 shrink-0 border-r border-line overflow-auto p-2">
            {loading && <div className="text-2xs text-muted p-2">加载中…</div>}
            {!loading && actions.length === 0 && (
              <div className="text-2xs text-muted p-2 leading-relaxed">
                此连接没有控制台动作。控制台动作由插件声明；SQL 引擎通常没有。
              </div>
            )}
            {groups.map(([group, list]) => (
              <div key={group} className="mb-2">
                <div className="text-2xs text-muted uppercase tracking-wide px-2 py-1">{group}</div>
                {list.map(a => (
                  <button
                    key={a.id}
                    onClick={() => pick(a)}
                    className={`w-full text-left px-2 py-1.5 rounded text-xs flex items-center gap-1.5 ${
                      a.id === selectedID
                        ? 'bg-accent/15 text-ink'
                        : 'text-secondary hover:text-ink hover:bg-raised'
                    }`}
                  >
                    {a.danger && <FiAlertTriangle size={10} className="text-warning shrink-0" />}
                    <span className="truncate">{a.title}</span>
                  </button>
                ))}
              </div>
            ))}
          </div>

          {/* Form + result */}
          <div className="flex-1 min-w-0 flex flex-col">
            <div className="p-3 border-b border-line space-y-2">
              {params.length === 0 && selected && (
                <div className="text-2xs text-muted">该动作没有参数，直接运行。</div>
              )}
              {params.map(p => (
                <label key={p.name} className="block space-y-0.5">
                  <span className="text-2xs text-secondary">
                    {p.label || p.name}
                    {p.required && <span className="text-danger"> *</span>}
                  </span>
                  {p.kind === 'textarea' ? (
                    <textarea
                      className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-xs text-ink font-mono focus:outline-none focus:border-accent"
                      rows={3}
                      value={values[p.name] ?? ''}
                      onChange={e => setValues(v => ({ ...v, [p.name]: e.target.value }))}
                      aria-label={p.label || p.name}
                    />
                  ) : (
                    <input
                      type={p.kind === 'number' ? 'number' : 'text'}
                      className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-xs text-ink focus:outline-none focus:border-accent"
                      value={values[p.name] ?? ''}
                      onChange={e => setValues(v => ({ ...v, [p.name]: e.target.value }))}
                      aria-label={p.label || p.name}
                    />
                  )}
                </label>
              ))}
              <div className="flex items-center gap-2">
                <button
                  onClick={run}
                  disabled={!selected || busy}
                  className="px-2.5 py-1 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40 flex items-center gap-1"
                >
                  <FiPlay size={11} />
                  {busy ? '运行中…' : '运行'}
                </button>
                {selected?.danger && (
                  <span className="text-2xs text-warning flex items-center gap-1">
                    <FiAlertTriangle size={10} /> 会改变远端状态
                  </span>
                )}
              </div>
            </div>

            {error && (
              <div className="px-3 py-2 text-2xs text-danger border-b border-line" role="alert">
                {error}
              </div>
            )}

            <div className="flex-1 min-h-0 overflow-auto">
              {result && (
                <table className="w-full text-2xs border-collapse">
                  <thead className="bg-raised text-muted uppercase tracking-wide sticky top-0">
                    <tr>
                      {(result.columns ?? []).map((c, i) => (
                        <th key={`${c.name}-${i}`} className="text-left px-2 py-1.5 font-medium whitespace-nowrap">
                          {c.name}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {(result.rows ?? []).map((row, ri) => (
                      <tr key={ri} className="border-t border-line">
                        {(row ?? []).map((cell, ci) => (
                          <td key={ci} className="px-2 py-1 font-mono align-top whitespace-pre-wrap break-all">
                            {cellText(cell)}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {result && (result.rows?.length ?? 0) === 0 && (
                <div className="p-3 text-2xs text-muted">动作返回了 0 行。</div>
              )}
            </div>
            {result && (
              <div className="px-3 py-1.5 border-t border-line text-2xs text-muted">
                {(result.rows?.length ?? 0)} 行
                {result.executionTime ? ` · ${result.executionTime.toFixed(3)}s` : ''}
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};

export default PluginConsole;