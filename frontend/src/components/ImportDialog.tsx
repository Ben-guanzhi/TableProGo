import { useRef, useState } from 'react';
import { ImportFile, ImportOptions, ImportResult } from '../api';
import { TableRef } from '../types';
import { FiUpload, FiX } from 'react-icons/fi';
import toast from 'react-hot-toast';
import { useDialog } from '../dialog';

interface ImportDialogProps {
  target: TableRef;
  onClose: () => void;
  onImported: () => void;
}

// ImportDialog asks for a file path and the handful of options the backend
// supports (import_db.go). The path is typed rather than picked because the
// HTTP API runs in the same process but the browser file input only yields a
// sandboxed name, not a path the backend could open.
const ImportDialog: React.FC<ImportDialogProps> = ({ target, onClose, onImported }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);
  const [path, setPath] = useState('');
  const [options, setOptions] = useState<ImportOptions>({
    format: '',
    hasHeader: true,
    delimiter: ',',
    createTable: true,
    inferTypes: true,
    skipErrors: false,
    mode: 'append',
  });
  // Conflict keys stay a comma-separated string in the input; the API wants a list.
  const [upsertKeys, setUpsertKeys] = useState('');
  const isSQL = /\\.sql$/i.test(path.trim());
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [error, setError] = useState('');

  const run = async () => {
    if (!path.trim()) {
      toast.error('请输入文件路径');
      return;
    }
    setRunning(true);
    setError('');
    setResult(null);
    try {
      const res = await ImportFile({
        connId: target.connId,
        database: target.database,
        table: target.table,
        path: path.trim(),
        options: {
          ...options,
          upsertKeys: options.mode === 'upsert'
            ? upsertKeys.split(',').map(k => k.trim()).filter(Boolean)
            : undefined,
        },
      });
      setResult(res);
      toast.success(`已导入 ${res.rows} 行${res.skipped ? `，跳过 ${res.skipped}` : ''}`);
      onImported();
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      setError(message);
      toast.error(`导入失败：${message}`, { duration: 8000 });
    } finally {
      setRunning(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div ref={panelRef} role="dialog" aria-modal="true" aria-labelledby="import-dialog-title" className="bg-panel rounded-lg w-full max-w-lg shadow-xl">
        <div className="flex items-center justify-between px-6 py-4 border-b border-line">
          <h2 id="import-dialog-title" className="text-lg font-semibold">
            导入数据 → {target.table}
          </h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭对话框">
            <FiX size={20} />
          </button>
        </div>

        <div className="p-6 space-y-4">
          <div>
            <label htmlFor="import-path" className="block text-sm text-secondary mb-1">CSV / JSON 文件绝对路径</label>
            <input
              id="import-path"
              autoFocus
              type="text"
              className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="C:\\data\\orders.csv"
            />
            <p className="text-2xs text-muted mt-1">
              按扩展名区分 .csv / .json / .sql；.sql 会按语句顺序整脚本执行（忽略下方表相关选项）。
            </p>
          </div>

          {!isSQL && (
            <>
          <div className="grid grid-cols-2 gap-3">
            <label className="flex items-center gap-2 text-sm text-secondary">
              <input type="checkbox" checked={options.hasHeader} onChange={(e) => setOptions({ ...options, hasHeader: e.target.checked })} />
              CSV 首行是列名
            </label>
            <label className="flex items-center gap-2 text-sm text-secondary">
              <input type="checkbox" checked={options.createTable} onChange={(e) => setOptions({ ...options, createTable: e.target.checked })} />
              表不存在时自动建表
            </label>
            <label className="flex items-center gap-2 text-sm text-secondary">
              <input type="checkbox" checked={options.inferTypes} onChange={(e) => setOptions({ ...options, inferTypes: e.target.checked })} />
              推断列类型（INTEGER/REAL/TEXT）
            </label>
            <label className="flex items-center gap-2 text-sm text-secondary">
              <input type="checkbox" checked={options.skipErrors} onChange={(e) => setOptions({ ...options, skipErrors: e.target.checked })} />
              跳过失败行（否则整批回滚）
            </label>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label htmlFor="import-delimiter" className="block text-sm text-secondary mb-1">CSV 分隔符</label>
              <input
                id="import-delimiter"
                type="text"
                maxLength={1}
                className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                value={options.delimiter}
                onChange={(e) => setOptions({ ...options, delimiter: e.target.value || ',' })}
              />
            </div>
            <div>
              <label htmlFor="import-mode" className="block text-sm text-secondary mb-1">写入模式</label>
              <select
                id="import-mode"
                className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                value={options.mode}
                onChange={(e) => setOptions({ ...options, mode: e.target.value as ImportOptions['mode'] })}
              >
                <option value="append">追加</option>
                <option value="truncate">先清空表</option>
                <option value="upsert">存在则更新（upsert）</option>
              </select>
            </div>
          </div>

          {options.mode === 'upsert' && (
            <div>
              <label htmlFor="import-upsert-keys" className="block text-sm text-secondary mb-1">
                冲突键（逗号分隔，留空 = 表的主键）
              </label>
              <input
                id="import-upsert-keys"
                type="text"
                className="w-full px-3 py-2 bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
                value={upsertKeys}
                onChange={(e) => setUpsertKeys(e.target.value)}
                placeholder="id"
              />
              <p className="text-2xs text-muted mt-1">
                PostgreSQL / SQLite 生成 ON CONFLICT，MySQL 生成 ON DUPLICATE KEY UPDATE；SQL Server / Oracle 会明确报错，请改用追加。
              </p>
            </div>
          )}
            </>
          )}

          {error && (
            <div className="text-sm text-danger bg-danger/10 border border-danger/30 rounded px-3 py-2 whitespace-pre-wrap">
              {error}
            </div>
          )}
          {result && !error && (
            <div className="text-sm text-secondary bg-raised border border-line rounded px-3 py-2">
              已写入 {result.rows} 行，跳过 {result.skipped} 行，执行 {result.statements} 条语句
              {result.createdTable ? '，并创建了表' : ''}。
            </div>
          )}

          <div className="flex justify-end gap-3 pt-4 border-t border-line">
            <button type="button" className="px-4 py-2 rounded text-sm bg-raised hover:bg-emphasis text-ink" onClick={onClose}>
              关闭
            </button>
            <button
              type="button"
              className={`flex items-center px-4 py-2 rounded text-sm ${
                running ? 'bg-emphasis text-secondary' : 'bg-accent hover:bg-accent-hover text-on-accent'
              }`}
              onClick={run}
              disabled={running}
            >
              <FiUpload size={14} className="mr-2" />
              {running ? '导入中…' : '开始导入'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};

export default ImportDialog;
