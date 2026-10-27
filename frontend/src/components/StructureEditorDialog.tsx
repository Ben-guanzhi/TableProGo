import { useMemo, useRef, useState } from 'react';
import toast from 'react-hot-toast';
import { FiPlus, FiTrash2, FiX } from 'react-icons/fi';
import { ColumnEdit, ApplyStructureEdit, PreviewStructureEdit } from '../api';
import type { TableRef } from '../types';
import { useDialog } from '../dialog';
import {
  FK_ACTIONS,
  parseColumnList,
  type ForeignKeyEditRow,
  type IndexEditRow,
} from '../lib/structureEdit';

interface StructureEditorDialogProps {
  target: TableRef;
  columns: { name: string; type: string; nullable: boolean; defaultValue: string }[];
  indexes: IndexEditRow[];
  foreignKeys: ForeignKeyEditRow[];
  onApplied: () => void;
  onClose: () => void;
}

type Tab = 'columns' | 'indexes' | 'foreignKeys';

const TABS: { id: Tab; label: string }[] = [
  { id: 'columns', label: '列' },
  { id: 'indexes', label: '索引' },
  { id: 'foreignKeys', label: '外键' },
];

// StructureEditorDialog: add/drop/alter for columns, and (P2-7) create/drop/rebuild
// for secondary indexes and foreign keys. Every tab round-trips the catalog: the
// form holds the full desired list, the backend diffs it and renders the DDL
// through the kernel ddl package. Preview first, apply in one transaction.
const StructureEditorDialog: React.FC<StructureEditorDialogProps> = ({
  target,
  columns,
  indexes,
  foreignKeys,
  onApplied,
  onClose,
}) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);
  const [tab, setTab] = useState<Tab>('columns');
  const [rows, setRows] = useState<ColumnEdit[]>(() =>
    columns.map(c => ({ name: c.name, type: c.type, nullable: c.nullable, defaultValue: c.defaultValue })),
  );
  const [indexRows, setIndexRows] = useState<IndexEditRow[]>(indexes);
  const [fkRows, setFkRows] = useState<ForeignKeyEditRow[]>(foreignKeys);
  const [preview, setPreview] = useState<{ statements: string[]; warnings: string[] } | null>(null);
  const [busy, setBusy] = useState(false);

  const payload = useMemo(
    () => ({
      connId: target.connId,
      database: target.database,
      schema: target.schema,
      table: target.table,
      columns: rows,
      indexes: indexRows.map(r => ({ name: r.name, unique: r.unique, columns: parseColumnList(r.columnsText) })),
      foreignKeys: fkRows.map(r => ({
        name: r.name,
        columns: parseColumnList(r.columnsText),
        refSchema: r.refSchema,
        refTable: r.refTable,
        refColumns: parseColumnList(r.refColumnsText),
        onDelete: r.onDelete,
        onUpdate: r.onUpdate,
      })),
    }),
    [target, rows, indexRows, fkRows],
  );

  const patch = (i: number, next: Partial<ColumnEdit>) =>
    setRows(current => current.map((r, j) => (j === i ? { ...r, ...next } : r)));
  const patchIndex = (i: number, next: Partial<IndexEditRow>) =>
    setIndexRows(current => current.map((r, j) => (j === i ? { ...r, ...next } : r)));
  const patchFk = (i: number, next: Partial<ForeignKeyEditRow>) =>
    setFkRows(current => current.map((r, j) => (j === i ? { ...r, ...next } : r)));

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
  const input =
    'w-full bg-canvas border border-line rounded px-1.5 py-1 font-mono text-ink focus:outline-none focus:border-accent disabled:opacity-50';

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div ref={panelRef} role="dialog" aria-modal="true" className="bg-panel rounded-lg w-full max-w-3xl max-h-[85vh] shadow-xl flex flex-col">
        <div className="flex items-center justify-between px-5 py-3 border-b border-line shrink-0">
          <h2 className="text-sm font-semibold">编辑结构 · {target.table}</h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭">
            <FiX size={16} />
          </button>
        </div>

        <div className="px-5 pt-2 shrink-0 flex gap-1 border-b border-line">
          {TABS.map(t => (
            <button
              key={t.id}
              className={`px-3 py-1.5 text-xs rounded-t -mb-px border border-b-0 ${
                tab === t.id ? 'border-line bg-raised text-ink font-medium' : 'border-transparent text-secondary hover:text-ink'
              }`}
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          ))}
        </div>

        <div className="p-4 overflow-auto flex-1 space-y-3">
          {tab === 'columns' && (
            <>
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
                          className={input}
                        />
                      </td>
                      <td className={cell}>
                        <input
                          aria-label={`第 ${i + 1} 行类型`}
                          value={row.type}
                          onChange={e => patch(i, { type: e.target.value })}
                          className={input}
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
                          className={input}
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
            </>
          )}

          {tab === 'indexes' && (
            <>
              <table className="w-full text-xs border-collapse">
                <thead>
                  <tr className="text-secondary">
                    <th className="text-left px-1.5 py-1">索引名</th>
                    <th className="text-left px-1.5 py-1">唯一</th>
                    <th className="text-left px-1.5 py-1">列（逗号分隔）</th>
                    <th className="w-8" />
                  </tr>
                </thead>
                <tbody>
                  {indexRows.map((row, i) => (
                    <tr key={i} className={row.locked ? 'opacity-70' : row.isNew ? 'bg-success/5' : undefined}>
                      <td className={cell}>
                        <input
                          aria-label={`索引 ${i + 1} 名称`}
                          value={row.name}
                          disabled={row.locked}
                          onChange={e => patchIndex(i, { name: e.target.value })}
                          className={input}
                        />
                      </td>
                      <td className={cell}>
                        <input
                          type="checkbox"
                          aria-label={`索引 ${i + 1} 唯一`}
                          checked={row.unique}
                          disabled={row.locked}
                          onChange={e => patchIndex(i, { unique: e.target.checked })}
                        />
                      </td>
                      <td className={cell}>
                        <input
                          aria-label={`索引 ${i + 1} 列`}
                          value={row.columnsText}
                          disabled={row.locked}
                          title={row.reason}
                          onChange={e => patchIndex(i, { columnsText: e.target.value })}
                          className={input}
                        />
                        {row.reason && <div className="text-2xs text-muted mt-0.5">{row.reason}</div>}
                      </td>
                      <td className={cell}>
                        {!row.locked && (
                          <button
                            className="p-1 text-secondary hover:text-danger"
                            aria-label={`删除索引 ${row.name}`}
                            title="删除这个索引（DROP INDEX）"
                            onClick={() => setIndexRows(current => current.filter((_, j) => j !== i))}
                          >
                            <FiTrash2 size={12} />
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                  {indexRows.length === 0 && (
                    <tr>
                      <td colSpan={4} className="text-muted text-xs px-1.5 py-2">
                        这个表没有可编辑的二级索引。
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
              <button
                className="flex items-center gap-1 px-2 py-1 rounded text-xs text-secondary hover:text-ink hover:bg-raised"
                onClick={() =>
                  setIndexRows(current => [
                    ...current,
                    { name: `idx_new_${current.length + 1}`, unique: false, columnsText: '', isNew: true, locked: false } as IndexEditRow,
                  ])
                }
              >
                <FiPlus size={11} /> 加一个索引
              </button>
              <p className="text-2xs text-muted">
                移除行 = DROP INDEX；改动列或唯一性 = 重建（DROP + CREATE，没有引擎支持原地改索引）；
                新增行 = CREATE INDEX。主键与约束自动索引不在此编辑。
              </p>
            </>
          )}

          {tab === 'foreignKeys' && (
            <>
              <table className="w-full text-xs border-collapse">
                <thead>
                  <tr className="text-secondary">
                    <th className="text-left px-1.5 py-1">名称</th>
                    <th className="text-left px-1.5 py-1">本表列</th>
                    <th className="text-left px-1.5 py-1">引用表.列</th>
                    <th className="text-left px-1.5 py-1">ON DELETE</th>
                    <th className="text-left px-1.5 py-1">ON UPDATE</th>
                    <th className="w-8" />
                  </tr>
                </thead>
                <tbody>
                  {fkRows.map((row, i) => (
                    <tr key={i} className={row.isNew ? 'bg-success/5' : undefined}>
                      <td className={cell}>
                        <input
                          aria-label={`外键 ${i + 1} 名称`}
                          value={row.name}
                          onChange={e => patchFk(i, { name: e.target.value })}
                          className={input}
                        />
                      </td>
                      <td className={cell}>
                        <input
                          aria-label={`外键 ${i + 1} 本表列`}
                          value={row.columnsText}
                          placeholder="col1, col2"
                          onChange={e => patchFk(i, { columnsText: e.target.value })}
                          className={input}
                        />
                      </td>
                      <td className={cell}>
                        <div className="flex gap-1">
                          <input
                            aria-label={`外键 ${i + 1} 引用表`}
                            value={row.refTable}
                            placeholder="parent"
                            onChange={e => patchFk(i, { refTable: e.target.value })}
                            className={input}
                          />
                          <input
                            aria-label={`外键 ${i + 1} 引用列`}
                            value={row.refColumnsText}
                            placeholder="id"
                            onChange={e => patchFk(i, { refColumnsText: e.target.value })}
                            className={input}
                          />
                        </div>
                        {row.refSchema && <div className="text-2xs text-muted mt-0.5">schema: {row.refSchema}</div>}
                      </td>
                      <td className={cell}>
                        <select
                          aria-label={`外键 ${i + 1} ON DELETE`}
                          value={row.onDelete}
                          onChange={e => patchFk(i, { onDelete: e.target.value })}
                          className={input}
                        >
                          {FK_ACTIONS.map(a => (
                            <option key={a} value={a}>
                              {a}
                            </option>
                          ))}
                        </select>
                      </td>
                      <td className={cell}>
                        <select
                          aria-label={`外键 ${i + 1} ON UPDATE`}
                          value={row.onUpdate}
                          onChange={e => patchFk(i, { onUpdate: e.target.value })}
                          className={input}
                        >
                          {FK_ACTIONS.map(a => (
                            <option key={a} value={a}>
                              {a}
                            </option>
                          ))}
                        </select>
                      </td>
                      <td className={cell}>
                        <button
                          className="p-1 text-secondary hover:text-danger"
                          aria-label={`删除外键 ${row.name}`}
                          title="删除这个外键（DROP CONSTRAINT / DROP FOREIGN KEY）"
                          onClick={() => setFkRows(current => current.filter((_, j) => j !== i))}
                        >
                          <FiTrash2 size={12} />
                        </button>
                      </td>
                    </tr>
                  ))}
                  {fkRows.length === 0 && (
                    <tr>
                      <td colSpan={6} className="text-muted text-xs px-1.5 py-2">
                        这个表没有外键。
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
              <button
                className="flex items-center gap-1 px-2 py-1 rounded text-xs text-secondary hover:text-ink hover:bg-raised"
                onClick={() =>
                  setFkRows(current => [
                    ...current,
                    {
                      name: `fk_new_${current.length + 1}`,
                      columnsText: '',
                      refSchema: '',
                      refTable: '',
                      refColumnsText: '',
                      onDelete: 'NO ACTION',
                      onUpdate: 'NO ACTION',
                      isNew: true,
                    } as ForeignKeyEditRow,
                  ])
                }
              >
                <FiPlus size={11} /> 加一个外键
              </button>
              <p className="text-2xs text-muted">
                改动外键 = 删旧建新；SQLite 不能在已有表上改外键（需要重建表），这类改动会给出说明而不是假语句。
                引用列数必须与本表列数一致。
              </p>
            </>
          )}

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
