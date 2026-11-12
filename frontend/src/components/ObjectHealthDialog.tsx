import React, { useEffect, useRef, useState } from 'react';
import { FiAlertTriangle, FiCheck, FiCode, FiRefreshCw, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  ListInvalidObjects,
  ListObjectDependencies,
  OracleDependency,
  OracleObject,
  OracleObjectHealth,
  RecompileObject,
} from '../api';
import { useDialog } from '../dialog';

interface ObjectHealthDialogProps {
  connId: string;
  database?: string;
  schema?: string;
  onClose: () => void;
}

// W9: Oracle 无效对象与依赖.
//
// The reason this is a panel and not a context-menu item: a deployment that left
// objects broken produces a LIST, and the useful action is "recompile these",
// not "recompile the one I happen to be looking at".
//
// The one non-obvious behaviour, learned from a live instance: Oracle ACCEPTS
// ALTER … COMPILE even when the object cannot be compiled — the object stays
// INVALID and the reason lands in ALL_ERRORS. So the panel never claims success
// from the request; it re-reads the list, and a still-invalid object shows its
// compiler errors instead of a green tick.
const ObjectHealthDialog: React.FC<ObjectHealthDialogProps> = ({
  connId,
  database = '',
  schema = '',
  onClose,
}) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [health, setHealth] = useState<OracleObjectHealth | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState('');
  // Errors per object, kept after a recompile so the user can read why.
  const [errors, setErrors] = useState<Record<string, { line: number; position: number; text: string }[]>>({});
  const [deps, setDeps] = useState<{ object: string; rows: OracleDependency[] } | null>(null);

  const refresh = async () => {
    setLoading(true);
    try {
      setHealth(await ListInvalidObjects(connId, database, schema));
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId, database, schema]);

  const recompile = async (obj: OracleObject) => {
    setBusy(obj.name);
    try {
      const res = await RecompileObject({
        connId,
        database,
        schema,
        name: obj.name,
        type: obj.type,
      });
      if (res.error) {
        toast.error(res.error);
        return;
      }
      setErrors(prev => ({ ...prev, [obj.name]: res.errors ?? [] }));
      if (res.stillInvalid) {
        toast.error(`${obj.name} 仍然无效：重编译被接受，但对象没修好`);
      } else {
        toast.success(`${obj.name} 已重编译，对象有效`);
        // Only on real success remove it from the list: the list is the truth.
        setHealth(prev =>
          prev ? { ...prev, objects: (prev.objects ?? []).filter(o => o.name !== obj.name) } : prev,
        );
      }
      await refresh();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  const showDeps = async (obj: OracleObject) => {
    setBusy(obj.name);
    try {
      const res = await ListObjectDependencies(connId, schema, obj.name, database);
      setDeps({ object: obj.name, rows: res.dependencies ?? [] });
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  const objects = health?.objects ?? [];

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-6" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="objhealth-title"
        className="bg-panel rounded-lg w-full max-w-3xl max-h-[88vh] flex flex-col shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center px-5 py-3 border-b border-line shrink-0">
          <FiCode size={15} className="mr-2 text-accent" />
          <h2 id="objhealth-title" className="text-sm font-semibold">
            无效对象
          </h2>
          {health?.supported && (
            <span className="ml-2 text-2xs text-muted">{objects.length} 个</span>
          )}
          <button
            className="ml-auto mr-1 p-1 text-secondary hover:text-ink"
            onClick={() => void refresh()}
            aria-label="刷新无效对象"
            title="刷新"
          >
            <FiRefreshCw size={13} className={loading ? 'animate-spin' : ''} />
          </button>
          <button className="p-1 text-secondary hover:text-ink" onClick={onClose} aria-label="关闭无效对象面板">
            <FiX size={16} />
          </button>
        </div>

        <div className="flex-1 overflow-auto p-5 space-y-3">
          {health && !health.supported && (
            <p className="text-xs text-muted">{health.note || '仅 Oracle 支持无效对象检查'}</p>
          )}
          {health?.note && health.supported && (
            <p className="text-xs text-danger break-words">{health.note}</p>
          )}

          {health?.supported && objects.length === 0 && !health.note && (
            <p className="text-xs text-success flex items-center gap-1">
              <FiCheck size={12} /> 没有无效对象
            </p>
          )}

          {objects.map(obj => {
            const objErrors = errors[obj.name] ?? [];
            return (
              <div key={`${obj.type}:${obj.name}`} className="border border-line rounded p-2 space-y-1">
                <div className="flex items-center gap-2">
                  <span className="text-xs text-ink font-mono">{obj.name}</span>
                  <span className="text-2xs text-muted">{obj.type}</span>
                  <div className="ml-auto flex items-center gap-1">
                    <button
                      className="px-2 py-0.5 rounded text-2xs border border-line text-secondary hover:text-ink hover:bg-raised disabled:opacity-40"
                      onClick={() => void showDeps(obj)}
                      disabled={busy === obj.name}
                    >
                      依赖
                    </button>
                    <button
                      className="px-2 py-0.5 rounded text-2xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
                      onClick={() => void recompile(obj)}
                      disabled={busy === obj.name}
                    >
                      重编译
                    </button>
                  </div>
                </div>
                {objErrors.length > 0 && (
                  <div className="bg-danger/10 border border-danger/40 rounded p-1.5 space-y-0.5">
                    <p className="text-2xs font-semibold text-danger flex items-center gap-1">
                      <FiAlertTriangle size={10} /> 编译错误
                    </p>
                    {objErrors.map((e, i) => (
                      <p key={i} className="text-2xs text-secondary font-mono break-words">
                        {e.line > 0 ? `${e.line}:${e.position} ` : ''}
                        {e.text}
                      </p>
                    ))}
                  </div>
                )}
              </div>
            );
          })}

          {deps && (
            <div className="border-t border-line pt-2 space-y-1">
              <div className="flex items-center">
                <span className="text-2xs font-semibold text-secondary">
                  {deps.object} 的依赖（{deps.rows.length}）
                </span>
                <button
                  className="ml-auto text-2xs text-muted hover:text-ink"
                  onClick={() => setDeps(null)}
                >
                  收起
                </button>
              </div>
              {deps.rows.length === 0 && <p className="text-2xs text-muted">没有记录到依赖</p>}
              {deps.rows.map((d, i) => (
                <p key={i} className="text-2xs text-secondary break-all">
                  {d.direction === 'out' ? '→ 依赖' : '← 被依赖'}{' '}
                  <span className="font-mono text-ink">
                    {d.owner}.{d.name}
                  </span>{' '}
                  <span className="text-muted">({d.type})</span>
                </p>
              ))}
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

export default ObjectHealthDialog;
