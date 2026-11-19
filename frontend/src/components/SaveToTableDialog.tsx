import React, { useState } from 'react';
import toast from 'react-hot-toast';
import { SaveResultToTable } from '../api';

// SaveToTableDialog writes the grid's loaded rows into a target table (the
// "export to table" path). Two decisions belong to the user: the table name and
// whether the table is created here (all columns TEXT — the target column types
// are the user's to define in the schema, not something a grid export guesses).
//
// The rows that are on screen are what gets written: the grid's view (sorted and
// filtered), exactly like the CSV export. A truncated result carries only the
// loaded rows — the caller warns about that before opening this dialog.

interface SaveToTableDialogProps {
  connId: string;
  database: string;
  columns: { name: string; type?: string }[];
  rows: { v: string; n?: boolean }[][];
  defaultTable: string;
  onClose: () => void;
}

const SaveToTableDialog: React.FC<SaveToTableDialogProps> = ({
  connId,
  database,
  columns,
  rows,
  defaultTable,
  onClose,
}) => {
  const [table, setTable] = useState(defaultTable);
  const [create, setCreate] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const run = async () => {
    const name = table.trim();
    if (!name) {
      setError('表名不能为空');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const res = await SaveResultToTable(connId, database, name, create, columns, rows);
      toast.success(`已写入 ${res.rows} 行到 ${res.table}`);
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40"
      onPointerDown={e => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className="w-[420px] bg-panel border border-line-strong rounded-md shadow-xl p-4 text-xs text-ink">
        <div className="text-sm font-semibold mb-3">保存结果到表</div>
        <label className="block mb-3">
          <span className="block text-2xs text-secondary mb-1">目标表名</span>
          <input
            value={table}
            onChange={e => setTable(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Enter') void run();
              if (e.key === 'Escape') onClose();
            }}
            className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-xs focus:outline-none focus:border-accent"
            aria-label="目标表名"
            autoFocus
          />
        </label>
        <label className="flex items-center gap-2 mb-3">
          <input
            type="checkbox"
            checked={create}
            onChange={e => setCreate(e.target.checked)}
            className="w-3.5 h-3.5 accent-accent"
          />
          <span>表不存在时创建（列类型全部 TEXT）</span>
        </label>
        <div className="text-2xs text-muted mb-3">
          将写入 {rows.length} 行 × {columns.length} 列（当前视图：已排序/已过滤的行）。
          {rows.length === 0 && ' 当前没有可写入的行。'}
        </div>
        {error && <div className="text-danger mb-3">{error}</div>}
        <div className="flex justify-end gap-2">
          <button
            onClick={onClose}
            className="px-2.5 py-1 rounded text-xs border border-line-strong hover:bg-canvas"
          >
            取消
          </button>
          <button
            onClick={() => void run()}
            disabled={busy || rows.length === 0}
            className="px-2.5 py-1 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
          >
            {busy ? '写入中…' : '写入'}
          </button>
        </div>
      </div>
    </div>
  );
};

export default SaveToTableDialog;
