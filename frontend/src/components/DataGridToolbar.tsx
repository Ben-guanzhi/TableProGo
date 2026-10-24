import {
  FiChevronDown,
  FiCopy,
  FiDownload,
  FiPlus,
  FiSearch,
  FiTrash2,
  FiUpload,
  FiX,
  FiXCircle,
} from 'react-icons/fi';
import { MenuItem } from './FilterMenu';

// The grid's tool strip: find-a-column, active view, row actions, import and the
// export menu. Split out of DataGrid so the grid's scrolling/editing body and
// this bar can be read on their own; every piece of state stays in DataGrid and
// arrives here as props, so the bar has no behaviour of its own.
//
// Exported view so the same shape is used by the object panel's data card and
// the query tab's result grid (they differ only in which props they can offer).

export interface DataGridToolbarProps {
  locate: string;
  onLocateChange: (value: string) => void;
  onLocateClear: () => void;
  onLocateNext: (total: number) => void;
  matchCount: number;
  activeMatch: number;

  sortLabel: string;
  filterCount: number;

  isFiltered: boolean;
  onClearViews: () => void;

  // Row actions exist only for an editable table.
  editable: boolean;
  onAppendRow: () => void;
  onDuplicateRow: () => void;
  onToggleDelete: () => void;
  selected: boolean;
  deleteQueued: boolean;

  onImport?: () => void;

  exportMenu: { left: number; top: number } | null;
  onExportToggle: (rect: DOMRect) => void;
  onExportClose: () => void;
  canExportSQL: boolean;
  onExport: (format: 'csv' | 'json' | 'markdown' | 'xlsx' | 'sql') => void;
  onCopyInserts: () => void;
}

const DataGridToolbar: React.FC<DataGridToolbarProps> = ({
  locate,
  onLocateChange,
  onLocateClear,
  onLocateNext,
  matchCount,
  activeMatch,
  sortLabel,
  filterCount,
  isFiltered,
  onClearViews,
  editable,
  onAppendRow,
  onDuplicateRow,
  onToggleDelete,
  selected,
  deleteQueued,
  onImport,
  exportMenu,
  onExportToggle,
  onExportClose,
  canExportSQL,
  onExport,
  onCopyInserts,
}) => (
  <div className="flex items-center gap-2 px-2 py-1 bg-panel border-b border-line shrink-0">
    <div className="relative">
      <FiSearch
        size={11}
        className="absolute left-2 top-1/2 -translate-y-1/2 text-muted pointer-events-none"
      />
      <input
        value={locate}
        onChange={e => onLocateChange(e.target.value)}
        onKeyDown={e => {
          if (e.key === 'Escape') onLocateClear();
          if (e.key === 'Enter' && matchCount) onLocateNext(matchCount);
        }}
        placeholder="定位列"
        aria-label="定位到指定列"
        className="w-44 bg-canvas border border-line rounded pl-6 pr-6 py-0.5 text-2xs text-ink placeholder-muted focus:outline-none focus:border-accent"
      />
      {locate && (
        <button
          className="absolute right-1 top-1/2 -translate-y-1/2 flex items-center justify-center w-5 h-5 text-muted hover:text-ink"
          onClick={onLocateClear}
          title="清空"
          aria-label="清空定位列的搜索词"
        >
          <FiX size={11} />
        </button>
      )}
    </div>
    {locate && (
      <span className="text-2xs text-muted">
        {matchCount ? `${Math.min(activeMatch + 1, matchCount)} / ${matchCount}` : '无匹配列'}
      </span>
    )}

    {isFiltered && (
      <button
        className="flex items-center gap-1 px-1.5 py-0.5 rounded text-2xs bg-raised text-secondary hover:text-ink"
        onClick={onClearViews}
        title="清除排序与所有列筛选"
      >
        <FiXCircle size={11} />
        {sortLabel}
        {sortLabel && filterCount ? ' · ' : ''}
        {filterCount ? `${filterCount} 个筛选` : ''}
      </button>
    )}

    {editable && (
      <div className="flex items-center gap-1 pl-2 border-l border-line">
        <button
          className="flex items-center gap-1 px-1.5 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
          onClick={onAppendRow}
          title="新增一行：只有填过的列会写入，其余列交给数据库默认值"
        >
          <FiPlus size={11} />
          新增行
        </button>
        <button
          className="flex items-center gap-1 px-1.5 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised disabled:opacity-40 disabled:cursor-not-allowed"
          onClick={onDuplicateRow}
          disabled={!selected}
          title="复制选中的行：值一起带过来，主键留空交给数据库"
        >
          <FiCopy size={11} />
          复制行
        </button>
        <button
          className={`flex items-center gap-1 px-1.5 py-0.5 rounded text-2xs disabled:opacity-40 disabled:cursor-not-allowed ${
            deleteQueued
              ? 'text-warning hover:text-ink hover:bg-raised'
              : 'text-secondary hover:text-danger hover:bg-raised'
          }`}
          onClick={onToggleDelete}
          disabled={!selected}
          title={deleteQueued ? '撤销这条删除标记' : '标记删除选中的行，应用前可以撤销'}
        >
          <FiTrash2 size={11} />
          {deleteQueued ? '撤销删除' : '删除行'}
        </button>
      </div>
    )}

    <div className="ml-auto relative flex items-center gap-1">
      {onImport && (
        <button
          className="flex items-center gap-1 px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
          title="从 CSV / JSON 导入到这张表"
          onClick={onImport}
        >
          <FiUpload size={11} />
          导入
        </button>
      )}
      <button
        className="flex items-center gap-1 px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
        aria-haspopup="true"
        aria-expanded={!!exportMenu}
        title="导出当前视图：CSV / JSON / Markdown / SQL"
        onPointerDown={e => e.stopPropagation()}
        onClick={e => onExportToggle(e.currentTarget.getBoundingClientRect())}
      >
        <FiDownload size={11} />
        导出
        <FiChevronDown size={10} aria-hidden />
      </button>
      {exportMenu && (
        <div
          className="fixed z-50 w-[190px] bg-panel border border-line-strong rounded-md shadow-xl py-1 text-[0.75rem]"
          aria-label="导出格式"
          style={{
            left: Math.max(4, Math.min(exportMenu.left, window.innerWidth - 198)),
            top: Math.max(4, Math.min(exportMenu.top, window.innerHeight - 240)),
          }}
          onPointerDown={e => e.stopPropagation()}
        >
          <MenuItem label="CSV" onClick={() => onExport('csv')} />
          <MenuItem label="JSON" onClick={() => onExport('json')} />
          <MenuItem label="Markdown 表格" onClick={() => onExport('markdown')} />
          <MenuItem label="Excel（XLSX）" onClick={() => onExport('xlsx')} />
          <div className="my-1 border-t border-line-soft" />
          <MenuItem label="SQL（INSERT 文件）" disabled={!canExportSQL} onClick={() => onExport('sql')} />
          <MenuItem label="复制为 INSERT" disabled={!canExportSQL} onClick={onCopyInserts} />
          {!canExportSQL && (
            <div className="px-2.5 pt-1 text-2xs text-muted leading-snug">
              当前结果没有来源表（视图或自定义查询），生成不了 INSERT
            </div>
          )}
        </div>
      )}
    </div>
  </div>
);

export default DataGridToolbar;
