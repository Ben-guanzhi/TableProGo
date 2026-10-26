import { useEffect, useRef, useState } from 'react';
import { FiAlertCircle, FiLoader, FiTag, FiX } from 'react-icons/fi';
import { useDialog } from '../dialog';
import { GetEnumTypes } from '../api';
import type { EnumTypeInfo } from '../types';

interface EnumsTarget {
  connId: string;
  database: string;
  schema?: string;
  title: string;
}

interface EnumsDialogProps {
  target: EnumsTarget;
  onClose: () => void;
}

// EnumsDialog lists one schema's user-defined enum types with their labels in
// declaration order (PostgreSQL only; dbcore.ExtraMetadataReader.EnumTypes).
//
// It is the schema-object counterpart of the routines dialog: the menu entry is
// only offered for PostgreSQL, and an engine that answers ErrNotSupported still
// shows the reason rather than an empty "nothing found" list.
const EnumsDialog: React.FC<EnumsDialogProps> = ({ target, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [enums, setEnums] = useState<EnumTypeInfo[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = () => {
    setLoading(true);
    setError('');
    GetEnumTypes(target.connId, target.database, target.schema ?? '')
      .then(list => setEnums(list ?? []))
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  };

  useEffect(load, [target.connId, target.database, target.schema]);

  const where = [target.database, target.schema].filter(Boolean).join('.');

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        className="bg-panel rounded-lg w-full max-w-2xl max-h-[75vh] shadow-xl flex flex-col"
      >
        <div className="flex items-center justify-between px-5 py-3 border-b border-line">
          <h2 className="text-sm font-semibold">
            枚举类型{where ? <span className="text-muted font-normal"> · {where}</span> : null}
          </h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭">
            <FiX size={16} />
          </button>
        </div>

        <div className="flex-1 min-h-0 overflow-auto">
          {loading && (
            <div className="flex items-center gap-2 p-5 text-secondary text-sm">
              <FiLoader className="animate-spin" size={14} />
              <span>读取枚举类型中…</span>
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

          {!loading && !error && enums && enums.length === 0 && (
            <div className="p-5 text-muted text-sm">该 schema 没有用户定义枚举类型。</div>
          )}

          {!loading && !error && enums && enums.length > 0 && (
            <ul className="divide-y divide-line/60">
              {enums.map(e => (
                <li key={e.name} className="px-4 py-2.5">
                  <div className="flex items-center gap-2">
                    <FiTag size={12} className="text-secondary shrink-0" />
                    <span className="font-mono text-xs text-ink">{e.name}</span>
                    <span className="text-2xs text-muted">{e.labels.length} 个标签</span>
                  </div>
                  <div className="mt-1.5 flex flex-wrap gap-1">
                    {e.labels.map(label => (
                      <span
                        key={label}
                        className="px-1.5 py-0.5 rounded bg-raised text-2xs font-mono text-ink"
                        title={label}
                      >
                        {label}
                      </span>
                    ))}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
};

export default EnumsDialog;
