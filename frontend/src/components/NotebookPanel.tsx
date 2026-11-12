import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  FiAlertTriangle,
  FiBarChart2,
  FiChevronDown,
  FiChevronUp,
  FiCode,
  FiPlay,
  FiPlus,
  FiSave,
  FiTrash2,
  FiX,
} from 'react-icons/fi';
import toast from 'react-hot-toast';
import {
  DeleteNotebook,
  LoadNotebooks,
  Notebook,
  NotebookCell,
  NotebookRunResult,
  RunNotebookCell,
  SaveNotebook,
} from '../api';
import { useDialog } from '../dialog';
import {
  cellProblem,
  depsOf,
  moveCell,
  newCellId,
  problemText,
  referencesOf,
} from '../lib/notebook';
import { CHART_HEIGHT, CHART_WIDTH, barRects, linePath, pickChart } from '../datagrid/chart';

interface NotebookPanelProps {
  connId: string;
  database?: string;
  onClose: () => void;
}

// W11b: SQL 笔记本.
//
// A notebook is a column of cells against one connection, and a cell may build on an
// earlier one with {{cell_N}}. The expansion is SERVER-side (the server decides what
// runs); this panel holds the cells' own text so the references stay visible, and
// shows the expanded statement on demand — when a cell surprises you, that is the
// answer.
const NotebookPanel: React.FC<NotebookPanelProps> = ({ connId, database = '', onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [saved, setSaved] = useState<Notebook[]>([]);
  const [current, setCurrent] = useState<Notebook | null>(null);
  const [runs, setRuns] = useState<Record<string, NotebookRunResult>>({});
  const [busy, setBusy] = useState('');
  const [showStatement, setShowStatement] = useState<string>('');

  const refresh = async () => {
    try {
      const res = await LoadNotebooks();
      const list = res.notebooks ?? [];
      setSaved(list);
      setCurrent(prev => prev ?? list.find(n => n.connection === connId) ?? null);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const cells = current?.cells ?? [];

  const create = () => {
    setCurrent({
      id: `nb-${Date.now()}`,
      name: '未命名笔记本',
      connection: connId,
      database,
      cells: [{ id: 'c1', sql: '' }],
    });
  };

  const updateCell = (index: number, sql: string) => {
    if (!current) return;
    const next = current.cells.slice();
    next[index] = { ...next[index], sql };
    setCurrent({ ...current, cells: next });
  };

  const addCell = () => {
    if (!current) return;
    setCurrent({ ...current, cells: [...current.cells, { id: newCellId(current.cells), sql: '' }] });
  };

  const removeCell = (index: number) => {
    if (!current) return;
    setCurrent({ ...current, cells: current.cells.filter((_, i) => i !== index) });
  };

  const move = (index: number, delta: number) => {
    if (!current) return;
    setCurrent({ ...current, cells: moveCell(current.cells, index, index + delta) });
  };

  const runCell = async (index: number) => {
    if (!current) return;
    const problem = cellProblem(current.cells, index);
    if (problem) {
      toast.error(problemText(problem));
      return;
    }
    const key = current.cells[index].id;
    setBusy(key);
    try {
      const res = await RunNotebookCell(current, index);
      setRuns(prev => ({ ...prev, [key]: res }));
      if (res.error) toast.error(res.error);
      else if (res.result.error) toast.error(res.result.error);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  const save = async () => {
    if (!current) return;
    try {
      const res = await SaveNotebook(current);
      setSaved(res.notebooks ?? []);
      toast.success('笔记本已保存');
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const remove = async () => {
    if (!current) return;
    try {
      const res = await DeleteNotebook(current.id);
      setSaved(res.notebooks ?? []);
      setCurrent(null);
      toast.success('已删除');
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50 p-4" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="notebook-title"
        className="bg-panel rounded-lg w-full max-w-4xl h-[88vh] flex flex-col shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-4 py-2.5 border-b border-line shrink-0">
          <FiCode size={15} className="text-accent" />
          <h2 id="notebook-title" className="text-sm font-semibold">
            笔记本
          </h2>
          {current && (
            <input
              aria-label="笔记本名称"
              className="ml-1 bg-canvas border border-line-strong rounded px-2 py-0.5 text-xs text-ink w-48 focus:outline-none focus:border-accent"
              value={current.name}
              onChange={e => setCurrent({ ...current, name: e.target.value })}
            />
          )}
          <select
            aria-label="已保存的笔记本"
            className="ml-auto bg-canvas border border-line rounded px-2 py-0.5 text-2xs text-ink max-w-[180px]"
            value={current?.id ?? ''}
            onChange={e => {
              const found = saved.find(n => n.id === e.target.value);
              if (found) setCurrent(found);
            }}
          >
            <option value="">（新建 / 选择）</option>
            {saved.map(n => (
              <option key={n.id} value={n.id}>
                {n.name}
              </option>
            ))}
          </select>
          <button
            className="p-1 text-secondary hover:text-ink"
            onClick={create}
            title="新建笔记本"
            aria-label="新建笔记本"
          >
            <FiPlus size={14} />
          </button>
          <button
            className="p-1 text-secondary hover:text-ink disabled:opacity-40"
            onClick={() => void save()}
            disabled={!current}
            title="保存"
            aria-label="保存笔记本"
          >
            <FiSave size={14} />
          </button>
          <button
            className="p-1 text-secondary hover:text-danger disabled:opacity-40"
            onClick={() => void remove()}
            disabled={!current}
            title="删除"
            aria-label="删除笔记本"
          >
            <FiTrash2 size={14} />
          </button>
          <button className="p-1 text-secondary hover:text-ink" onClick={onClose} aria-label="关闭笔记本">
            <FiX size={16} />
          </button>
        </div>

        <div className="flex-1 overflow-auto p-4 space-y-3">
          {!current && (
            <div className="text-center py-10 space-y-2">
              <p className="text-xs text-muted">还没有打开笔记本。</p>
              <button
                className="px-3 py-1.5 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover"
                onClick={create}
              >
                新建一个
              </button>
            </div>
          )}

          {current &&
            cells.map((c, i) => {
              const problem = cellProblem(cells, i);
              const run = runs[c.id];
              const chart = run && !run.result.error ? pickChart(run.result.columns.map(x => x.name), run.result.rows) : null;
              const refs = referencesOf(c.sql);
              const deps = depsOf(cells, i);
              return (
                <div key={c.id} className="border border-line rounded overflow-hidden">
                  <div className="flex items-center gap-1.5 px-2 py-1 bg-raised border-b border-line">
                    <span className="text-2xs font-mono text-muted">[{i + 1}]</span>
                    <span className="text-2xs text-secondary truncate">
                      {deps.length > 0 ? `依赖 ${deps.map(d => `cell_${d}`).join(', ')}` : '独立单元格'}
                    </span>
                    <div className="ml-auto flex items-center gap-0.5">
                      <button
                        className="p-1 text-secondary hover:text-ink disabled:opacity-30"
                        onClick={() => move(i, -1)}
                        disabled={i === 0}
                        aria-label={`上移单元格 ${i + 1}`}
                      >
                        <FiChevronUp size={12} />
                      </button>
                      <button
                        className="p-1 text-secondary hover:text-ink disabled:opacity-30"
                        onClick={() => move(i, 1)}
                        disabled={i === cells.length - 1}
                        aria-label={`下移单元格 ${i + 1}`}
                      >
                        <FiChevronDown size={12} />
                      </button>
                      <button
                        className="p-1 text-secondary hover:text-danger"
                        onClick={() => removeCell(i)}
                        aria-label={`删除单元格 ${i + 1}`}
                      >
                        <FiTrash2 size={12} />
                      </button>
                      <button
                        className="px-2 py-0.5 ml-1 rounded text-2xs bg-success text-on-success hover:bg-success-hover disabled:opacity-40 flex items-center gap-1"
                        onClick={() => void runCell(i)}
                        disabled={busy === c.id}
                        aria-label={`运行单元格 ${i + 1}`}
                      >
                        <FiPlay size={10} />
                        运行
                      </button>
                    </div>
                  </div>

                  <textarea
                    aria-label={`单元格 ${i + 1} 的 SQL`}
                    className="w-full bg-canvas text-ink font-mono text-xs p-2 resize-y focus:outline-none"
                    rows={3}
                    spellCheck={false}
                    value={c.sql}
                    placeholder={`SELECT …${i > 0 ? `（可用 ${refs.length ? '' : '{{cell_1}} '}引用前面的单元格）` : ''}`}
                    onChange={e => updateCell(i, e.target.value)}
                  />

                  {problem && (
                    <p className="px-2 pb-1 text-2xs text-warning flex items-center gap-1">
                      <FiAlertTriangle size={10} />
                      {problemText(problem)}
                    </p>
                  )}

                  {run?.error && <p className="px-2 pb-2 text-2xs text-danger">{run.error}</p>}
                  {run && !run.error && (
                    <div className="border-t border-line px-2 py-1.5 space-y-1">
                      <div className="flex items-center gap-2 text-2xs text-muted">
                        <span>
                          {run.result.error
                            ? '失败'
                            : `${run.result.rowCount} 行 · ${run.result.executionTime.toFixed(3)}s`}
                        </span>
                        <button
                          className="ml-auto text-secondary hover:text-ink"
                          onClick={() => setShowStatement(showStatement === c.id ? '' : c.id)}
                        >
                          {showStatement === c.id ? '隐藏实际语句' : '查看实际语句'}
                        </button>
                      </div>
                      {run.result.error && <p className="text-2xs text-danger break-words">{run.result.error}</p>}
                      {showStatement === c.id && (
                        <pre className="text-2xs font-mono text-secondary whitespace-pre-wrap break-words bg-canvas rounded p-1.5 max-h-32 overflow-auto">
                          {run.statement}
                        </pre>
                      )}
                      {!run.result.error && run.result.columns.length > 0 && (
                        <div className="overflow-auto max-h-40">
                          <table className="text-2xs w-full">
                            <thead>
                              <tr>
                                {run.result.columns.map(col => (
                                  <th key={col.name} className="text-left px-1.5 py-0.5 text-muted font-normal">
                                    {col.name}
                                  </th>
                                ))}
                              </tr>
                            </thead>
                            <tbody>
                              {run.result.rows.slice(0, 20).map((row, ri) => (
                                <tr key={ri} className="border-t border-line-soft">
                                  {row.map((v, ci) => (
                                    <td key={ci} className="px-1.5 py-0.5 text-ink font-mono truncate max-w-[14rem]">
                                      {v === null || v === undefined ? 'NULL' : String(v)}
                                    </td>
                                  ))}
                                </tr>
                              ))}
                            </tbody>
                          </table>
                        </div>
                      )}
                      {chart && (
                        <div className="pt-1">
                          <div className="text-2xs text-muted flex items-center gap-1">
                            <FiBarChart2 size={10} />
                            {chart.valueColumn} × {chart.labelColumn}
                          </div>
                          <svg width={CHART_WIDTH} height={CHART_HEIGHT} role="img" aria-label="结果图表">
                            {chart.kind === 'bar'
                              ? barRects(chart).map((r, i) => (
                                  <rect
                                    key={i}
                                    x={r.x}
                                    y={r.y}
                                    width={r.w}
                                    height={r.h}
                                    fill="rgb(var(--accent) / 0.7)"
                                  >
                                    <title>{`${r.label}: ${r.value}`}</title>
                                  </rect>
                                ))
                              : (
                                  <path d={linePath(chart)} fill="none" stroke="rgb(var(--accent))" strokeWidth={1.5} />
                                )}
                          </svg>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              );
            })}

          {current && (
            <button
              className="w-full py-1.5 rounded text-2xs border border-dashed border-line-strong text-secondary hover:text-ink hover:bg-raised"
              onClick={addCell}
            >
              + 添加单元格
            </button>
          )}
        </div>

        <div className="px-4 py-2 border-t border-line text-2xs text-muted shrink-0">
          引用写法：<span className="font-mono">{'{{cell_1}}'}</span>（只能引用前面的单元格；运行时会展开成
          CTE）。写操作单元格不能引用其它单元格。
        </div>
      </div>
    </div>
  );
};

export default NotebookPanel;
