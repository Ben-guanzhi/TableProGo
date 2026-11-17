import React, { useEffect, useRef, useState } from 'react';
import { FiRefreshCw, FiX } from 'react-icons/fi';
import { ListServerVariables, ServerVar } from '../api';
import { useDialog } from '../dialog';

interface ServerVarsDialogProps {
  connId: string;
  database: string;
  onClose: () => void;
}

// 服务器变量查看（Navicat「变量」/ HeidiSQL Variables 面板的对齐）：
//
// **只读**——内核 ServerVariablesReader 没有改值，面板不出编辑框。过滤框
// （名字/值）+ 名字排序列表；与默认值不同标 *（改了未生效的暗示）；选中行
// 底部看详情（默认/范围/引擎说明）。引擎不支持时给一行说明。
const ServerVarsDialog: React.FC<ServerVarsDialogProps> = ({ connId, database, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  const [rows, setRows] = useState<ServerVar[] | null>(null);
  const [error, setError] = useState('');
  const [filter, setFilter] = useState('');
  const [selected, setSelected] = useState<ServerVar | null>(null);

  useEffect(() => {
    ListServerVariables(connId, database)
      .then(setRows)
      .catch((e: Error) => setError(e.message));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connId, database]);

  const term = filter.trim().toLowerCase();
  const visible = (rows ?? []).filter(
    v =>
      term === '' ||
      v.name.toLowerCase().includes(term) ||
      v.value.toLowerCase().includes(term),
  );

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40" onClick={onClose}>
      <div
        ref={panelRef}
        className="bg-surface border border-border rounded-lg shadow-xl w-[780px] max-h-[580px] flex flex-col"
        onClick={e => e.stopPropagation()}
        role="dialog"
        aria-label="Server variables"
      >
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border">
          <span className="font-semibold text-sm">Server Variables</span>
          <span className="text-xs text-dim">
            {rows ? `${visible.length}/${rows.length}（只读）` : '加载中…'}
          </span>
          <div className="flex-1" />
          <input
            className="text-xs border border-border rounded px-2 py-1 w-56 bg-transparent"
            placeholder="过滤变量名/值…"
            value={filter}
            onChange={e => setFilter(e.target.value)}
            aria-label="过滤变量"
          />
          <button className="tool-btn" onClick={onClose} title="关闭">
            <FiX /> 关闭
          </button>
        </div>
        {error ? (
          <div className="px-4 py-3 text-sm text-dim">{error}</div>
        ) : (
          <>
            <div className="flex-1 overflow-auto px-4 py-2">
              {!rows && <div className="text-sm text-dim py-4">加载中…</div>}
              {rows && visible.length === 0 && (
                <div className="text-sm text-dim py-4">没有匹配的变量。</div>
              )}
              {rows &&
                visible.map(v => (
                  <div
                    key={v.name}
                    onClick={() => setSelected(v)}
                    className={`flex items-baseline gap-2 py-1 px-2 cursor-pointer rounded ${
                      selected?.name === v.name ? 'bg-selected' : 'hover:bg-hover'
                    }`}
                  >
                    <span className="font-mono text-xs font-semibold shrink-0">{v.name}</span>
                    <span className="text-xs truncate" title={v.value}>
                      {v.value}
                      {v.default && v.value !== v.default && (
                        <span className="text-warning font-bold"> *</span>
                      )}
                    </span>
                  </div>
                ))}
            </div>
            {selected && (
              <div className="border-t border-border px-4 py-2 text-xs whitespace-pre-wrap">
                <div>
                  <span className="text-dim">值: </span>
                  <span className="font-mono">{selected.value}</span>
                  {selected.default && selected.value !== selected.default && (
                    <span className="text-dim">（默认: {selected.default}——当前与默认不同）</span>
                  )}
                </div>
                {selected.range && (
                  <div className="text-dim">范围: {selected.range}</div>
                )}
                {selected.desc && <div className="text-dim">{selected.desc}</div>}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
};

export default ServerVarsDialog;
