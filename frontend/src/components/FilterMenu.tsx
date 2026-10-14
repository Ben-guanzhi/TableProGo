import React from 'react';
import { FiArrowDown, FiArrowUp, FiCheck, FiFilter } from 'react-icons/fi';
import { ColumnFilter, FilterOp, filterLabels, opNeedsValue } from '../gridView';

interface MenuItemProps {
  label: string;
  icon?: React.ReactNode;
  active?: boolean;
  disabled?: boolean;
  onClick: () => void;
}

export const MenuItem: React.FC<MenuItemProps> = ({ label, icon, active, disabled, onClick }) => (
  <button
    className={`w-full flex items-center gap-2 px-2.5 py-1 text-left hover:bg-raised disabled:opacity-40 ${
      active ? 'text-accent' : 'text-ink'
    }`}
    onClick={onClick}
    disabled={disabled}
  >
    <span className="w-3.5 shrink-0 flex items-center justify-center">
      {active ? <FiCheck size={11} /> : icon}
    </span>
    <span className="truncate">{label}</span>
  </button>
);

interface FilterMenuProps {
  x: number;
  y: number;
  column: string;
  op: FilterOp | null;
  filter: ColumnFilter | undefined;
  sorted: 'asc' | 'desc' | null;
  filterDraft: string;
  onFilterDraftChange: (v: string) => void;
  onApplyFilter: (op: FilterOp) => void;
  onSort: (dir: 'asc' | 'desc') => void;
  onClearSort: () => void;
  onPickFilter: (op: FilterOp) => void;
  onClearFilter: () => void;
  onClose: () => void;
}

const FilterMenu: React.FC<FilterMenuProps> = ({
  x,
  y,
  column,
  op,
  filter,
  sorted,
  filterDraft,
  onFilterDraftChange,
  onApplyFilter,
  onSort,
  onClearSort,
  onPickFilter,
  onClearFilter,
  onClose,
}) => {
  const valueEntry = op && opNeedsValue(op) ? op : null;

  return (
    <div
      className="fixed z-50 w-[210px] bg-panel border border-line-strong rounded-md shadow-xl py-1 text-[0.75rem]"
      style={{
        left: Math.max(4, Math.min(x, window.innerWidth - 220)),
        top: Math.max(4, Math.min(y, window.innerHeight - 260)),
      }}
      onPointerDown={e => e.stopPropagation()}
    >
      <div className="px-2.5 pb-1 text-2xs text-muted truncate border-b border-line-soft">
        {column}
      </div>

      {valueEntry ? (
        <div className="px-2.5 py-2">
          <div className="text-2xs text-secondary mb-1">
            {column} {filterLabels[valueEntry]} …
          </div>
          <input
            autoFocus
            aria-label={`筛选 ${column} 的值`}
            value={filterDraft}
            onChange={e => onFilterDraftChange(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Enter') onApplyFilter(valueEntry);
            }}
            placeholder="用于筛选的值"
            className="w-full bg-canvas border border-accent rounded px-1.5 py-0.5 text-[0.75rem] text-ink focus:outline-none"
          />
          <div className="flex items-center justify-end gap-1 mt-1.5">
            <button
              className="px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
              onClick={onClose}
            >
              取消
            </button>
            <button
              className="px-2 py-0.5 rounded text-2xs bg-accent text-on-accent hover:bg-accent-hover"
              onClick={() => onApplyFilter(valueEntry)}
            >
              筛选
            </button>
          </div>
        </div>
      ) : (
        <>
          <MenuItem
            label="升序排序"
            icon={<FiArrowUp size={11} />}
            active={sorted === 'asc'}
            onClick={() => onSort('asc')}
          />
          <MenuItem
            label="降序排序"
            icon={<FiArrowDown size={11} />}
            active={sorted === 'desc'}
            onClick={() => onSort('desc')}
          />
          <MenuItem
            label="清除排序"
            disabled={!sorted}
            onClick={onClearSort}
          />
          <div className="my-1 border-t border-line-soft" />
          {(['eq', 'ne', 'contains', 'null', 'notNull'] as FilterOp[]).map(f => (
            <MenuItem
              key={f}
              label={`筛选：${filterLabels[f]}`}
              icon={<FiFilter size={10} />}
              active={filter?.op === f}
              onClick={() => onPickFilter(f)}
            />
          ))}
          <MenuItem
            label="清除该列筛选"
            disabled={!filter}
            onClick={onClearFilter}
          />
        </>
      )}
    </div>
  );
};

export default FilterMenu;
