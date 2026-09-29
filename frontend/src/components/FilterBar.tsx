import React, { useEffect, useState } from 'react';
import { FiFilter, FiPlus, FiX } from 'react-icons/fi';
import {
  addFilter,
  describeFilter,
  dropFilter,
  filterIsComplete,
  opsForType,
  sqlFilterLabels,
} from '../sqlFilters';
import type { SqlFilter, SqlFilterOp } from '../types';

interface FilterBarProps {
  // The columns the bar may name, with the types the result reported: the type
  // decides which comparisons are offered.
  columns: { name: string; type: string }[];
  filters: SqlFilter[];
  // A query is in flight, so another condition would only queue up behind it.
  busy?: boolean;
  onChange: (next: SqlFilter[]) => void;
}

const controlClass =
  'bg-canvas border border-line rounded px-1.5 py-0.5 text-2xs text-ink focus:outline-none focus:border-accent disabled:opacity-50';

// The conditions run on the server and re-read the page, which is how a filter
// reaches rows that never came to the client. The column-header filter is the
// other kind: it only hides rows already on screen.
const FilterBar: React.FC<FilterBarProps> = ({ columns, filters, busy, onChange }) => {
  const [column, setColumn] = useState('');
  const [op, setOp] = useState<SqlFilterOp>('eq');
  const [value, setValue] = useState('');

  const typeOf = columns.find(col => col.name === column)?.type ?? '';
  const ops = opsForType(typeOf);

  // A column that cannot be compared the way the draft says is a stale draft,
  // not a condition to send.
  useEffect(() => {
    if (!ops.includes(op)) setOp('eq');
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [column]);

  const draft: SqlFilter = { column: column || columns[0]?.name || '', op, value };
  const canAdd = !!draft.column && filterIsComplete(draft) && !busy;

  const submit = () => {
    if (!canAdd) return;
    onChange(addFilter(filters, { ...draft, value: value.trim() }));
    setValue('');
  };

  return (
    <div className="flex items-center gap-2 px-3 py-1 bg-panel border-b border-line text-2xs text-muted shrink-0 flex-wrap">
      <span className="flex items-center gap-1" title="条件在数据库执行，重新查询整页">
        <FiFilter size={11} />
        服务端条件
      </span>
      <select
        className={controlClass}
        value={draft.column}
        onChange={e => setColumn(e.target.value)}
        disabled={busy}
        aria-label="筛选字段"
        title="字段"
      >
        {columns.map(col => (
          <option key={col.name} value={col.name}>
            {col.name}
          </option>
        ))}
      </select>
      <select
        className={controlClass}
        value={op}
        onChange={e => setOp(e.target.value as SqlFilterOp)}
        disabled={busy}
        aria-label="比较方式"
        title="比较方式"
      >
        {ops.map(name => (
          <option key={name} value={name}>
            {sqlFilterLabels[name]}
          </option>
        ))}
      </select>
      {op !== 'null' && op !== 'notNull' && (
        <input
          className={`${controlClass} w-36`}
          value={value}
          placeholder="值"
          aria-label="比较值"
          onChange={e => setValue(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter') {
              e.preventDefault();
              submit();
            }
            if (e.key === 'Escape') setValue('');
          }}
          disabled={busy}
          title="要比较的值；回车添加"
        />
      )}
      <button
        className="flex items-center gap-0.5 px-1.5 py-0.5 rounded text-secondary hover:text-ink hover:bg-raised disabled:opacity-40"
        onClick={submit}
        disabled={!canAdd}
        title="添加条件并重新查询"
      >
        <FiPlus size={11} />
        添加
      </button>
      {filters.length > 0 && (
        <>
          <div className="flex items-center gap-1 flex-wrap">
            {filters.map((filter, i) => (
              <span
                key={`${describeFilter(filter)}|${i}`}
                className="flex items-center gap-1 px-1.5 py-0.5 rounded bg-raised text-ink"
              >
                {describeFilter(filter)}
                <button
                  className="flex items-center justify-center w-5 h-5 shrink-0 text-muted hover:text-danger"
                  onClick={() => onChange(dropFilter(filters, i))}
                  disabled={busy}
                  aria-label={`移除条件 ${describeFilter(filter)}`}
                  title="移除"
                >
                  <FiX size={10} />
                </button>
              </span>
            ))}
          </div>
          <button
            className="px-1.5 py-0.5 rounded text-secondary hover:text-ink hover:bg-raised disabled:opacity-40"
            onClick={() => onChange([])}
            disabled={busy}
            title="清空所有条件并重新读取"
          >
            清空
          </button>
        </>
      )}
      {busy && <span className="text-muted">查询中…</span>}
    </div>
  );
};

export default FilterBar;
