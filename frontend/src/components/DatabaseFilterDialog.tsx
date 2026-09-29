import React, { useId, useRef, useState } from 'react';
import { FiAlertCircle, FiX } from 'react-icons/fi';
import { Connection, DatabaseFilter } from '../types';
import { useDialog } from '../dialog';

interface DatabaseFilterDialogProps {
  connection: Connection;
  onClose: () => void;
  onSave: (filter: DatabaseFilter) => void;
}

interface PatternListProps {
  label: string;
  placeholder: string;
  patterns: string[];
  onChange: (patterns: string[]) => void;
}

const PatternList: React.FC<PatternListProps> = ({ label, placeholder, patterns, onChange }) => {
  const [draft, setDraft] = useState('');
  const [selected, setSelected] = useState<number | null>(null);
  const fieldId = useId();

  const add = () => {
    const value = draft.trim();
    if (!value || patterns.includes(value)) {
      setDraft('');
      return;
    }
    onChange([...patterns, value]);
    setDraft('');
  };

  const removeSelected = () => {
    if (selected === null) return;
    onChange(patterns.filter((_, i) => i !== selected));
    setSelected(null);
  };

  return (
    <div>
      <label htmlFor={fieldId} className="block text-sm text-ink mb-1">{label}</label>
      <input
        id={fieldId}
        type="text"
        className="w-full px-3 py-1.5 text-sm bg-raised rounded border border-line-strong focus:border-accent focus:outline-none"
        value={draft}
        onChange={e => setDraft(e.target.value)}
        onKeyDown={e => {
          if (e.key === 'Enter') {
            e.preventDefault();
            add();
          }
        }}
        placeholder={placeholder}
      />
      <div className="flex items-start mt-1.5">
        <div
          className="flex-1 h-28 overflow-y-auto bg-canvas border border-line-strong rounded py-1"
          onDoubleClick={removeSelected}
        >
          {patterns.map((pattern, i) => (
            <div
              key={pattern}
              className={`px-3 py-0.5 text-sm font-mono cursor-pointer ${
                selected === i ? 'bg-accent text-on-accent' : 'text-ink hover:bg-raised'
              }`}
              onClick={() => setSelected(i)}
            >
              {pattern}
            </div>
          ))}
          {patterns.length === 0 && (
            <div className="px-3 py-1 text-xs text-muted italic">No patterns</div>
          )}
        </div>
        <div className="flex flex-col space-y-1.5 ml-2 w-20">
          <button
            type="button"
            className="px-2 py-1 text-xs bg-raised hover:bg-emphasis rounded"
            onClick={add}
          >
            Add
          </button>
          <button
            type="button"
            className="px-2 py-1 text-xs bg-raised hover:bg-emphasis rounded disabled:opacity-40 disabled:cursor-not-allowed"
            onClick={removeSelected}
            disabled={selected === null}
          >
            Remove
          </button>
          <button
            type="button"
            className="px-2 py-1 text-xs bg-raised hover:bg-emphasis rounded disabled:opacity-40 disabled:cursor-not-allowed"
            onClick={() => {
              onChange([]);
              setSelected(null);
            }}
            disabled={patterns.length === 0}
          >
            Clear
          </button>
        </div>
      </div>
    </div>
  );
};

const DatabaseFilterDialog: React.FC<DatabaseFilterDialogProps> = ({
  connection,
  onClose,
  onSave,
}) => {
  const [filter, setFilter] = useState<DatabaseFilter>({
    include: connection.databaseFilter?.include ?? [],
    exclude: connection.databaseFilter?.exclude ?? [],
  });
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div ref={panelRef} role="dialog" aria-modal="true" aria-labelledby="db-filter-title" className="bg-panel rounded-lg w-full max-w-lg shadow-xl">
        <div className="flex items-center justify-between px-5 py-3 border-b border-line">
          <h2 id="db-filter-title" className="text-sm font-semibold">Filtering: {connection.name}</h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭对话框">
            <FiX size={18} />
          </button>
        </div>

        <div className="p-5 space-y-4">
          <PatternList
            label="Include"
            placeholder="e.g. APP* or *_Env"
            patterns={filter.include}
            onChange={include => setFilter({ ...filter, include })}
          />

          <PatternList
            label="Exclude"
            placeholder="e.g. tempdb or *"
            patterns={filter.exclude}
            onChange={exclude => setFilter({ ...filter, exclude })}
          />

          <div className="space-y-1.5 text-xs text-secondary">
            <p className="flex items-start">
              <FiAlertCircle size={12} className="mr-1.5 mt-0.5 shrink-0 text-accent" />
              Patterns apply as soon as you save; clear both lists to show every database
            </p>
            <p className="flex items-start">
              <FiAlertCircle size={12} className="mr-1.5 mt-0.5 shrink-0 text-accent" />
              You can use masks (%, _ and *) in filters
            </p>
            <p className="flex items-start">
              <FiAlertCircle size={12} className="mr-1.5 mt-0.5 shrink-0 text-accent" />
              Exclude wins over Include; an empty Include list keeps everything not excluded
            </p>
            <p className="flex items-start">
              <FiAlertCircle size={12} className="mr-1.5 mt-0.5 shrink-0 text-accent" />
              Filtered databases are hidden in the Navigator, SQL still runs against the server
            </p>
          </div>
        </div>

        <div className="flex justify-end space-x-3 px-5 py-4 border-t border-line">
          <button
            type="button"
            className="px-4 py-1.5 rounded text-sm bg-raised hover:bg-emphasis text-ink"
            onClick={onClose}
          >
            Cancel
          </button>
          <button
            type="button"
            className="px-4 py-1.5 rounded text-sm bg-accent hover:bg-accent-hover text-on-accent"
            onClick={() => onSave(filter)}
          >
            OK
          </button>
        </div>
      </div>
    </div>
  );
};

export default DatabaseFilterDialog;
