import { useEffect, useRef, useState } from 'react';
import { FiAlertCircle, FiCode, FiLoader, FiX } from 'react-icons/fi';
import { useDialog } from '../dialog';
import { GetRoutineDefinition, GetRoutines } from '../api';
import type { RoutineInfo } from '../types';

interface RoutinesTarget {
  connId: string;
  database: string;
  schema?: string;
  title: string;
}

interface RoutinesDialogProps {
  target: RoutinesTarget;
  onClose: () => void;
}

// RoutinesDialog lists one schema's stored functions and procedures (kernel
// capability dbcore.ExtraMetadataReader) and opens a definition on demand.
//
// A routine is a schema-level object, so it has no table to hang a structure
// card on: the list lives here, reached from the database/schema context menu.
// Engines without the concept (SQLite) and plugin connections that predate the
// capability answer an error with the reason — shown as text, never as an
// empty "nothing found" list.
const RoutinesDialog: React.FC<RoutinesDialogProps> = ({ target, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [routines, setRoutines] = useState<RoutineInfo[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [definition, setDefinition] = useState<{ name: string; text: string } | null>(null);
  const [opening, setOpening] = useState('');

  const load = () => {
    setLoading(true);
    setError('');
    GetRoutines(target.connId, target.database, target.schema ?? '')
      .then(list => setRoutines(list ?? []))
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  };

  useEffect(load, [target.connId, target.database, target.schema]);

  const openDefinition = (name: string) => {
    setOpening(name);
    GetRoutineDefinition(target.connId, target.database, target.schema ?? '', name)
      .then(text => setDefinition({ name, text }))
      .catch((e: Error) => setError(e.message))
      .finally(() => setOpening(''));
  };

  const where = [target.database, target.schema].filter(Boolean).join('.');

  return (
    <>
      <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
        <div
          ref={panelRef}
          role="dialog"
          aria-modal="true"
          className="bg-panel rounded-lg w-full max-w-3xl max-h-[80vh] shadow-xl flex flex-col"
        >
          <div className="flex items-center justify-between px-5 py-3 border-b border-line">
            <h2 className="text-sm font-semibold">
              函数与过程{where ? <span className="text-muted font-normal"> · {where}</span> : null}
            </h2>
            <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭">
              <FiX size={16} />
            </button>
          </div>

          <div className="flex-1 min-h-0 overflow-auto">
            {loading && (
              <div className="flex items-center gap-2 p-5 text-secondary text-sm">
                <FiLoader className="animate-spin" size={14} />
                <span>读取函数与过程中…</span>
              </div>
            )}

            {!loading && error && (
              <div className="p-5 flex items-start gap-2 text-danger text-sm">
                <FiAlertCircle size={14} className="mt-0.5 shrink-0" />
                <div>
                  <div className="whitespace-pre-wrap break-all">{error}</div>
                  <button className="mt-2 text-accent hover:underline text-2xs" onClick={load}>
                    重试
                  </button>
                </div>
              </div>
            )}

            {!loading && !error && routines && routines.length === 0 && (
              <div className="p-5 text-muted text-sm">该 schema 没有函数或过程。</div>
            )}

            {!loading && !error && routines && routines.length > 0 && (
              <table className="w-full text-xs border-collapse">
                <thead className="sticky top-0 bg-panel">
                  <tr className="border-b border-line">
                    <th className="text-left px-3 py-1.5 font-medium text-secondary">名称</th>
                    <th className="text-left px-3 py-1.5 font-medium text-secondary w-28">种类</th>
                    <th className="text-left px-3 py-1.5 font-medium text-secondary w-20">定义</th>
                  </tr>
                </thead>
                <tbody>
                  {routines.map(r => (
                    <tr key={`${r.kind}:${r.name}`} className="even:bg-row-alt/40 border-b border-line/50">
                      <td className="px-3 py-1.5 font-mono">{r.name}</td>
                      <td className="px-3 py-1.5 text-secondary">{r.kind}</td>
                      <td className="px-3 py-1.5">
                        <button
                          className="text-accent hover:underline text-2xs disabled:opacity-40"
                          disabled={!!opening}
                          onClick={() => openDefinition(r.name)}
                        >
                          {opening === r.name ? '读取中…' : '查看'}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      </div>

      {definition && (
        <div
          className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-[60]"
          onClick={() => setDefinition(null)}
        >
          <div
            className="bg-panel rounded-lg w-full max-w-3xl max-h-[75vh] shadow-xl flex flex-col"
            onClick={e => e.stopPropagation()}
            role="dialog"
            aria-modal="true"
          >
            <div className="flex items-center justify-between px-5 py-3 border-b border-line">
              <h2 className="text-sm font-semibold font-mono flex items-center gap-2">
                <FiCode size={13} className="text-secondary" />
                {definition.name}
              </h2>
              <button className="text-secondary hover:text-ink" onClick={() => setDefinition(null)} aria-label="关闭">
                <FiX size={16} />
              </button>
            </div>
            <pre className="flex-1 overflow-auto p-4 text-xs font-mono text-ink whitespace-pre-wrap break-all">
              {definition.text}
            </pre>
          </div>
        </div>
      )}
    </>
  );
};

export default RoutinesDialog;
