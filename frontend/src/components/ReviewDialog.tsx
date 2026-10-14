import React from 'react';
import { FiCheck, FiLoader, FiX } from 'react-icons/fi';

interface ReviewDialogProps {
  targetName: string;
  pendingLabel: string;
  statements: string[];
  counts: { inserts: number; deletes: number; updates: number; total: number };
  applying: boolean;
  applyError: string;
  onApply: () => void;
  onClose: () => void;
}

const ReviewDialog: React.FC<ReviewDialogProps> = ({
  targetName,
  pendingLabel,
  statements,
  counts,
  applying,
  applyError,
  onApply,
  onClose,
}) => (
  <div
    className="fixed inset-0 z-50 bg-scrim/50 flex items-center justify-center p-6"
    onClick={() => {
      if (!applying) onClose();
    }}
  >
    <div
      className="bg-panel border border-line-strong rounded-lg w-full max-w-2xl shadow-xl"
      onClick={e => e.stopPropagation()}
    >
      <header className="flex items-center px-4 py-2.5 border-b border-line">
        <h2 className="text-sm font-semibold">应用 {pendingLabel}</h2>
        <button
          className="ml-auto flex items-center justify-center w-6 h-6 text-secondary hover:text-ink"
          onClick={onClose}
          aria-label="关闭改动确认对话框"
          disabled={applying}
        >
          <FiX size={16} />
        </button>
      </header>

      <div className="px-4 py-3 space-y-2">
        <p className="text-xs text-secondary leading-relaxed">
          将在 <span className="font-mono text-ink">{targetName}</span> 上按顺序执行{' '}
          {statements.length} 条语句，整体放在一个事务里：任何一条失败就全部回滚，不会留下改到一半的数据。
        </p>
        {counts.deletes > 0 && (
          <p className="text-xs text-danger leading-relaxed">
            其中 {counts.deletes} 行会被删除。删除按主键定位，主键在读取后被改过就整批回滚。
          </p>
        )}
        {counts.inserts > 0 && (
          <p className="text-xs text-secondary leading-relaxed">
            新增行只写入你填过的列，其余列交给数据库的默认值或自增。
          </p>
        )}
        <ol className="max-h-[45vh] overflow-auto bg-canvas border border-line-strong rounded p-2 space-y-1">
          {statements.map((statement, i) => (
            <li
              key={i}
              className="font-mono text-[0.75rem] leading-relaxed text-ink whitespace-pre-wrap break-all"
            >
              {i + 1}. {statement}
            </li>
          ))}
        </ol>
        {applyError && (
          <p className="text-xs text-danger">
            未提交任何改动：{applyError}
          </p>
        )}
      </div>

      <footer className="flex items-center justify-end gap-2 px-4 py-3 border-t border-line">
        <button
          className="px-3 py-1 rounded text-xs bg-raised hover:bg-emphasis disabled:opacity-50"
          onClick={onClose}
          disabled={applying}
        >
          取消
        </button>
        <button
          className="flex items-center px-3 py-1 rounded text-xs bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-50"
          onClick={onApply}
          disabled={applying || statements.length === 0}
          title="写入数据库，不可撤销"
        >
          {applying ? (
            <FiLoader size={12} className="mr-1 animate-spin" />
          ) : (
            <FiCheck size={12} className="mr-1" />
          )}
          {applying ? '应用中…' : '确认应用'}
        </button>
      </footer>
    </div>
  </div>
);

export default ReviewDialog;
