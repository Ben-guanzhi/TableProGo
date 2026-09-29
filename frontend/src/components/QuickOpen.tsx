import React, { useEffect, useRef, useState } from 'react';
import { FiCornerDownLeft, FiEye, FiLoader, FiSearch, FiTable } from 'react-icons/fi';
import { ObjectMatch } from '../api';
import { useDialog } from '../dialog';

interface QuickOpenProps {
  // Only the names are needed to label each hit; the ref carries the id.
  connections: { id: string; name: string }[];
  search: (pattern: string) => Promise<ObjectMatch[]>;
  onPick: (match: ObjectMatch) => void;
  onClose: () => void;
}

const fullName = (match: ObjectMatch) =>
  [match.database, match.schema, match.table].filter(Boolean).join('.');

// The whole-app name search: type a fragment, open the table it names.
const QuickOpen: React.FC<QuickOpenProps> = ({ connections, search, onPick, onClose }) => {
  const [text, setText] = useState('');
  const [results, setResults] = useState<ObjectMatch[]>([]);
  const [loading, setLoading] = useState(false);
  // Why the list is empty when the catalog itself could not be read; an empty
  // result and a failed one must not look the same.
  const [failure, setFailure] = useState('');
  const [active, setActive] = useState(0);
  const panelRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef(search);
  searchRef.current = search;
  useDialog(panelRef, onClose);

  const names = new Map(connections.map(c => [c.id, c.name]));

  // Keystrokes arrive far faster than a catalog walk does, so the search waits
  // for them to settle and drops a response the typing has already outrun.
  useEffect(() => {
    const pattern = text.trim();
    if (!pattern) {
      setResults([]);
      setFailure('');
      setLoading(false);
      return;
    }
    setLoading(true);
    let current = true;
    const timer = setTimeout(() => {
      searchRef.current(pattern).then(
        list => {
          if (!current) return;
          setResults(list);
          setFailure('');
          setActive(0);
          setLoading(false);
        },
        err => {
          if (!current) return;
          setResults([]);
          setFailure(err?.message ?? String(err));
          setLoading(false);
        },
      );
    }, 220);
    return () => {
      current = false;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text]);

  useEffect(() => {
    const row = listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`);
    row?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      onClose();
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActive(i => (results.length ? (i + 1) % results.length : 0));
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActive(i => (results.length ? (i - 1 + results.length) % results.length : 0));
    }
    if (e.key === 'Enter' && results[active]) {
      e.preventDefault();
      onPick(results[active]);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 bg-scrim/40 flex items-start justify-center pt-[12vh] px-6"
      onClick={onClose}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="快速打开表或视图"
        className="w-full max-w-xl bg-panel border border-line-strong rounded-lg shadow-xl overflow-hidden"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-3 border-b border-line">
          <FiSearch size={14} className="shrink-0 text-muted" />
          <input
            autoFocus
            aria-label="按名称搜索表或视图"
            value={text}
            onChange={e => setText(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="按名称打开表 / 视图"
            className="flex-1 bg-transparent py-2.5 text-sm text-ink placeholder-muted focus:outline-none"
          />
          {loading && <FiLoader size={13} className="shrink-0 animate-spin text-muted" />}
          <kbd className="shrink-0 text-2xs text-muted border border-line rounded px-1 py-0.5">Esc</kbd>
        </div>

        <div ref={listRef} className="max-h-[46vh] overflow-auto py-1">
          {!text.trim() && (
            <div className="px-3 py-6 text-center text-muted text-xs">
              输入名称片段即可，搜索所有已连接库的表与视图
            </div>
          )}
          {text.trim() && !loading && failure && (
            <div className="px-3 py-6 text-center text-danger text-xs break-words">{failure}</div>
          )}
          {text.trim() && !loading && !failure && results.length === 0 && (
            <div className="px-3 py-6 text-center text-muted text-xs">没有匹配的表或视图</div>
          )}
          {results.map((match, i) => (
            <button
              key={`${match.connId}|${fullName(match)}`}
              data-index={i}
              className={`w-full flex items-center gap-2 px-3 py-1.5 text-left ${
                i === active ? 'bg-accent/20' : 'hover:bg-raised'
              }`}
              onMouseEnter={() => setActive(i)}
              onClick={() => onPick(match)}
              title={fullName(match)}
            >
              {match.type === 'view' ? (
                <FiEye size={12} className="shrink-0 text-icon-view" />
              ) : (
                <FiTable size={12} className="shrink-0 text-success" />
              )}
              <span className="font-mono text-[0.8125rem] text-ink truncate">{match.table}</span>
              <span className="text-2xs text-muted truncate">
                {match.schema ? `${match.database}.${match.schema}` : match.database}
              </span>
              <span className="ml-auto pl-2 text-2xs text-secondary truncate shrink-0">
                {names.get(match.connId)}
              </span>
              {i === active && <FiCornerDownLeft size={11} className="shrink-0 text-accent" />}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
};

export default QuickOpen;
