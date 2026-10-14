import React from 'react';
import { FiLoader } from 'react-icons/fi';
import { ForeignKeyCandidates, ForeignKeyValue } from '../api';
import { describeCandidate } from '../gridView';
import { optionId, PICKER_HEIGHT } from '../datagrid/picker';

interface FKPickerState {
  read?: ForeignKeyCandidates;
  list: ForeignKeyValue[];
  active: number;
  loading: boolean;
}

interface FKPickerPanelProps {
  left: number;
  top: number;
  width: number;
  column: string;
  error: string;
  pickerState: FKPickerState;
  onPick: (value: string) => void;
}

const FKPickerPanel: React.FC<FKPickerPanelProps> = ({
  left,
  top,
  width,
  column,
  error,
  pickerState: { read, list, active, loading },
  onPick,
}) => (
  <div
    className="fixed z-50 flex flex-col bg-panel border border-line-strong rounded-md shadow-xl text-[0.75rem] text-ink overflow-hidden"
    style={{ left, top, width, height: PICKER_HEIGHT }}
    onMouseDown={e => {
      e.preventDefault();
      e.stopPropagation();
    }}
  >
    <div className="shrink-0 px-2 py-1 text-2xs text-muted border-b border-line-soft truncate">
      {read ? `引用 ${read.refTable}.${read.refColumn}` : column}
      {read?.labelColumn ? ` · ${read.labelColumn}` : ''}
    </div>
    <div
      role="listbox"
      aria-label={`${column} 的候选值`}
      className="flex-1 overflow-y-auto py-0.5"
    >
      {loading && (
        <div className="flex items-center gap-1.5 px-2 py-1 text-muted">
          <FiLoader size={11} aria-hidden className="animate-spin" />
          读取候选值…
        </div>
      )}
      {error && <div className="px-2 py-1 text-danger break-all">{error}</div>}
      {!loading && !error && list.length === 0 && (
        <div className="px-2 py-1 text-muted">没有匹配的值，可直接输入</div>
      )}
      {list.map((candidate, i) => (
        <div
          key={candidate.value}
          id={optionId(i)}
          role="option"
          aria-selected={i === active}
          className={`px-2 py-0.5 cursor-pointer truncate ${
            i === active ? 'bg-accent/25' : 'hover:bg-raised'
          }`}
          title={describeCandidate(candidate)}
          onClick={() => onPick(candidate.value)}
        >
          {describeCandidate(candidate)}
        </div>
      ))}
    </div>
    {read?.truncated && (
      <div className="shrink-0 px-2 py-1 text-2xs text-muted border-t border-line-soft">
        引用表只读了前 {read.values.length} 条，其它值可直接输入
      </div>
    )}
  </div>
);

export default FKPickerPanel;
