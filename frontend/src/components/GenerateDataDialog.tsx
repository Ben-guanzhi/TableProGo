import React, { useState } from 'react';
import toast from 'react-hot-toast';
import { GenerateData } from '../api';
import type { TableRef } from '../types';

// GenerateDataDialog asks the kernel for type-driven test INSERTs and puts
// them in the SQL editor — nothing runs until the user reviews and runs it
// (the same "generate, then confirm" discipline as rename/truncate).
//
// A seed makes the output reproducible: the same seed and column list always
// yield the same values, which matters when a fixture must be recreated.

interface GenerateDataDialogProps {
  target: TableRef;
  onInsert: (sql: string) => void;
  onClose: () => void;
}

const GenerateDataDialog: React.FC<GenerateDataDialogProps> = ({ target, onInsert, onClose }) => {
  const [rows, setRows] = useState(10);
  const [seed, setSeed] = useState(0);
  const [skipPK, setSkipPK] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const run = async () => {
    setBusy(true);
    setError('');
    try {
      const res = await GenerateData(
        target.connId,
        target.database,
        target.schema || '',
        target.table,
        rows,
        seed,
        skipPK,
      );
      // Put the generated INSERTs at the editor cursor (the same bridge the
      // snippet/sidebar insert uses). Nothing executes until the user runs it.
      onInsert(res.statements.join('\n'));
      toast.success(`已生成 ${res.rows} 条 INSERT（seed ${res.seed}）——已放入编辑器，确认后再运行`);
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
      <div className="w-[440px] bg-panel border border-line-strong rounded-md shadow-xl p-4 text-xs text-ink">
        <div className="text-sm font-semibold mb-3">生成测试数据 · {target.table}</div>
        <div className="grid grid-cols-2 gap-3 mb-3">
          <label>
            <span className="block text-2xs text-secondary mb-1">行数（1–10000）</span>
            <input
              type="number"
              min={1}
              max={10000}
              value={rows}
              onChange={e => setRows(Number(e.target.value))}
              className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-xs focus:outline-none focus:border-accent"
              aria-label="生成行数"
            />
          </label>
          <label>
            <span className="block text-2xs text-secondary mb-1">种子（0 = 随机）</span>
            <input
              type="number"
              min={0}
              value={seed}
              onChange={e => setSeed(Number(e.target.value))}
              className="w-full bg-canvas border border-line-strong rounded px-2 py-1 text-xs focus:outline-none focus:border-accent"
              aria-label="随机种子"
            />
          </label>
        </div>
        <label className="flex items-center gap-2 mb-3">
          <input
            type="checkbox"
            checked={skipPK}
            onChange={e => setSkipPK(e.target.checked)}
            className="w-3.5 h-3.5 accent-accent"
          />
          <span>跳过主键列（自增/序列由服务器填）</span>
        </label>
        <div className="text-2xs text-muted mb-3">
          按列的声明类型生成（整数 / 小数 / 布尔 / 日期 / UUID / 文本按列名猜语义）。
          语句进编辑器后由你确认运行。
        </div>
        {error && <div className="text-danger mb-3">{error}</div>}
        <div className="flex justify-end gap-2">
          <button onClick={onClose} className="px-2.5 py-1 rounded text-xs border border-line-strong hover:bg-canvas">
            取消
          </button>
          <button
            onClick={() => void run()}
            disabled={busy || rows <= 0}
            className="px-2.5 py-1 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
          >
            {busy ? '生成中…' : '生成 INSERT'}
          </button>
        </div>
      </div>
    </div>
  );
};

export default GenerateDataDialog;
