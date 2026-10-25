import { useMemo, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import { FiPlus, FiTrash2, FiX } from 'react-icons/fi';
import { ColumnEdit, ApplyStructureEdit, PreviewStructureEdit } from '../api';
import type { TableRef } from '../types';
import { useDialog } from '../dialog';

interface StructureEditorDialogProps {
  target: TableRef;
  columns: { name: string; type: string; nullable: boolean; defaultValue: string }[];
  onApplied: () => void;
  onClose: () => void;
}

// StructureEditorDialog: column-level add/drop/alter. The form round-trips the
// catalog; the backend diffs it and renders the ALTER statements through the
// kernel ddl package. Preview first, apply in one transaction.
const StructureEditorDialog: React.FC<StructureEditorDialogProps> = ({
  target,
  columns,
  onApplied,
  onClose,
}) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);
  const [rows, setRows] = useState<ColumnEdit[]>(() =>
    columns.map(c => ({ name: c.name, type: c.type, nullable: c.nullable, defaultValue: c.defaultValue })),
  );
  const [preview, setPreview] = useState<{ statements: string[]; warnings: string[] } | null>(null);
  const [busy, setBusy] = useState(false);

  const payload = useMemo(
    () => ({
      connId: target.connId,
      database: target.database,
      schema: target.schema,
      table: target.table,
      columns: rows,
    }),
    [target, rows],
  );

  const patch = (i: number, next: Partial<ColumnEdit>) =>
    setRows(current => current.map((r, j) => (j === i ? { ...r, ...next } : r)));

  const runPreview = async () => {
    setBusy(true);
    try {
      const res = await PreviewStructureEdit(payload);
      setPreview(res);
      if (res.statements.length === 0 && res.warnings.length === 0) {
        toast.success('没有需要执行的改动');
      }
    } catch (e) {
      toast.error((e as Error).message, { duration: 8000 });
    } finally {
      setBusy(false);
    }
  };

  const runApply = async () => {
    setBusy(true);
    try {
      const res = await ApplyStructureEdit(payload);
      setPreview(res);
      toast.success(`结构已更新（${res.statements.length} 条语句）`);
      onApplied();
      onClose();
    } catch (e) {
      toast.error((e as Error).message, { duration: 10000 });
    } finally {
      setBusy(false);
    }
  };

  const cell = 'px-1.5 py-1 border-b border-line-soft';
  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div ref={panelRef} role="dialog" aria-modal="true" className="bg-panel rounded-lg w-full max-w-2xl max-h-[85vh] shadow-xl flex flex-col">
        <div className="flex items-center justify-between px-5 py-3 border-b border-line shrink-0">
          <h2 className="text-sm font-semibold">编辑结构 · {target.table}</h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭">
            <FiX size={16} />
          </button>
        </div>

        <div className="p-4 overflow-auto flex-1 space-y-3">
          <table className="w-full text-xs border-collapse">
            <thead>
              <tr className="text-secondary">
                <th className="text-left px-1.5 py-1">列名</th>
                <th className="text-left px-1.5 py-1">类型</th>
                <th className="text-left px-1.5 py-1">可空</th>
                <th className="text-left px-1.5 py-1">默认值</th>
                <th className="w-8" />
              </tr>
            </thead>
            <tbody>
              {rows.map((row, i) => (
                <tr key={i} className={row.isNew ? 'bg-success/5' : undefined}>
                  <td className={cell}>
                    <input
                      aria-label={`第 ${i + 1} 行列名`}
                      value={row.name}
                      onChange={e => patch(i, { name: e.target.value })}
                      className="w-full bg-canvas border border-line rounded px-1.5 py-1 font-mono text-ink focus:outline-none focus:border-accent"
                    />
                  </td>
                  <td className={cell}>
                    <input
                      aria-label={`第 ${i + 1} 行类型`}
                      value={row.type}
                      onChange={e => patch(i, { type: e.target.value })}
                      className="w-full bg-canvas border border-line rounded px-1.5 py-1 font-mono text-ink focus:outline-none focus:border-accent"
                    />
                  </td>
                  <td className={cell}>
                    <input
                      type="checkbox"
                      aria-label={`第 ${i + 1} 行可空`}
                      checked={row.nullable}
                      onChange={e => patch(i, { nullable: e.target.checked })}
                    />
                  </td>
                  <td className={cell}>
                    <input
                      aria-label={`第 ${i + 1} 行默认值`}
                      value={row.defaultValue}
                      onChange={e => patch(i, { defaultValue: e.target.value })}
                      placeholder="NULL"
                      className="w-full bg-canvas border border-line rounded px-1.5 py-1 font-mono text-ink focus:outline-none focus:border-accent"
                    />
                  </td>
                  <td className={cell}>
                    <button
                      className="p-1 text-secondary hover:text-danger"
                      aria-label={`删除列 ${row.name}`}
                      title="从表里删除这一列（DROP COLUMN）"
                      onClick={() => setRows(current => current.filter((_, j) => j !== i))}
                    >
                      <FiTrash2 size={12} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <button
            className="flex items-center gap-1 px-2 py-1 rounded text-xs text-secondary hover:text-ink hover:bg-raised"
            onClick={() =>
              setRows(current => [...current, { name: `column_${current.length + 1}`, type: 'TEXT', nullable: true, defaultValue: '', isNew: true }])
            }
          >
            <FiPlus size={11} /> 加一列
          </button>
          <p className="text-2xs text-muted">
            移除表单里的行 = DROP COLUMN；新增行 = ADD COLUMN；改动类型/可空/默认值 = ALTER COLUMN
            （引擎不支持时给出说明而不是假语句）。执行在一个事务里。
          </p>

          {preview && preview.warnings.length > 0 && (
            <ul className="px-3 py-2 rounded bg-warning/10 border border-warning/40 text-warning text-xs list-disc list-inside">
              {preview.warnings.map((w, i) => (
                <li key={i}>{w}</li>
              ))}
            </ul>
          )}
          {preview && preview.statements.length > 0 && (
            <div>
              <div className="text-2xs text-secondary mb-1">将执行 {preview.statements.length} 条语句：</div>
              <pre className="max-h-40 overflow-auto px-3 py-2 rounded bg-canvas border border-line text-2xs font-mono text-ink whitespace-pre-wrap break-all">
                {preview.statements.join(';\n\n')}
              </pre>
            </div>
          )}
        </div>

        <div className="flex justify-end gap-3 px-5 py-3 border-t border-line shrink-0">
          <button className="px-4 py-2 rounded text-sm bg-raised hover:bg-emphasis text-ink" onClick={onClose}>
            关闭
          </button>
          <button
            className="px-4 py-2 rounded text-sm bg-raised hover:bg-emphasis text-ink disabled:opacity-40"
            onClick={() => void runPreview()}
            disabled={busy}
          >
            {busy ? '计算中…' : '预览改动'}
          </button>
          <button
            className="px-4 py-2 rounded text-sm bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-40"
            onClick={() => void runApply()}
            disabled={busy || !preview || preview.statements.length === 0}
            title={preview?.statements.length === 0 ? '没有可执行的改动' : '在一个事务里执行这些语句'}
          >
            执行
          </button>
        </div>
      </div>
    </div>
  );
};

export default StructureEditorDialog;
