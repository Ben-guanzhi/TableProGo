import { useRef, useState } from 'react';
import toast from 'react-hot-toast';
import { FiX } from 'react-icons/fi';
import { useDialog } from '../dialog';
import { BackupDatabase } from '../api';

interface BackupTarget {
  connId: string;
  dbType: string;
  database: string;
  schema?: string;
}

interface BackupDialogProps {
  target: BackupTarget;
  onClose: () => void;
}

// BackupDialog drives the native-tool backup (pg_dump/mysqldump): mode, table
// scope, output path. The run is cancellable via the editor's cancel plumbing
// (the backend registers it under the connection), and the tool's stderr lands
// in the log pane line by line.
const BackupDialog: React.FC<BackupDialogProps> = ({ target, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);
  const [mode, setMode] = useState<'full' | 'structure' | 'data'>('full');
  const [tables, setTables] = useState('');
  const [outPath, setOutPath] = useState('');
  const [running, setRunning] = useState(false);
  const [log, setLog] = useState<string[]>([]);
  const [error, setError] = useState('');

  const tableList = tables
    .split(',')
    .map(t => t.trim())
    .filter(Boolean);

  const run = async () => {
    if (!outPath.trim()) {
      setError('请填写输出文件路径');
      return;
    }
    setRunning(true);
    setError('');
    setLog([]);
    try {
      const res = await BackupDatabase({
        connId: target.connId,
        database: target.database,
        mode,
        tables: tableList,
        outPath: outPath.trim(),
      });
      setLog(res.log ?? []);
      if (res.ok) {
        toast.success(`备份完成：${outPath.trim()}`);
      } else {
        setError(res.error || '备份失败');
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setRunning(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div ref={panelRef} role="dialog" aria-modal="true" className="bg-panel rounded-lg w-full max-w-md shadow-xl">
        <div className="flex items-center justify-between px-5 py-3 border-b border-line">
          <h2 className="text-sm font-semibold">备份数据库 {target.database}</h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭">
            <FiX size={16} />
          </button>
        </div>

        <div className="p-5 space-y-3">
          <p className="text-2xs text-muted">
            使用官方工具 {target.dbType === 'postgresql' ? 'pg_dump' : 'mysqldump'}（在 PATH 或环境变量
            TABLEPRO_PG_DUMP / TABLEPRO_MYSQLDUMP 指定的路径上查找）。
            口令经环境变量传递，不进入进程参数。
          </p>

          <div className="flex gap-3 text-xs">
            {(
              [
                ['full', '结构 + 数据'],
                ['structure', '仅结构'],
                ['data', '仅数据'],
              ] as const
            ).map(([value, label]) => (
              <label key={value} className="flex items-center gap-1.5 cursor-pointer">
                <input type="radio" name="backup-mode" checked={mode === value} onChange={() => setMode(value)} />
                {label}
              </label>
            ))}
          </div>

          <label className="block text-xs text-secondary">
            <span className="mb-1 block">表范围（逗号分隔，留空 = 整库）</span>
            <input
              type="text"
              value={tables}
              onChange={e => setTables(e.target.value)}
              placeholder="orders, users"
              className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink focus:outline-none focus:border-accent"
            />
          </label>

          <label className="block text-xs text-secondary">
            <span className="mb-1 block">输出文件路径</span>
            <input
              type="text"
              value={outPath}
              onChange={e => setOutPath(e.target.value)}
              placeholder={target.dbType === 'postgresql' ? 'C:\\backups\\app.sql' : 'C:\\backups\\app.sql'}
              className="w-full bg-canvas border border-line-strong rounded px-2 py-1.5 text-xs text-ink font-mono focus:outline-none focus:border-accent"
            />
          </label>

          {error && (
            <div className="px-3 py-2 rounded bg-danger/10 border border-danger/30 text-danger text-xs whitespace-pre-wrap break-all">
              {error}
            </div>
          )}

          {log.length > 0 && (
            <pre className="max-h-40 overflow-auto px-3 py-2 rounded bg-canvas border border-line text-2xs text-muted whitespace-pre-wrap break-all">
              {log.join('\n')}
            </pre>
          )}

          <div className="flex justify-end gap-3 pt-2">
            <button
              type="button"
              className="px-4 py-2 rounded text-sm bg-raised hover:bg-emphasis text-ink disabled:opacity-40"
              onClick={onClose}
              disabled={running}
            >
              关闭
            </button>
            <button
              type="button"
              className="px-4 py-2 rounded text-sm bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-40"
              onClick={() => void run()}
              disabled={running}
            >
              {running ? '备份中…' : '开始备份'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};

export default BackupDialog;
