import React, { Fragment, useEffect, useRef, useState } from 'react';
import { FiChevronDown, FiChevronRight, FiLoader, FiSearch, FiX } from 'react-icons/fi';
import DataGrid from './DataGrid';
import { useDialog } from '../dialog';
import { SearchDatabaseData } from '../api';
import type { SearchDataEvent } from '../api';
import type { Column, QueryResult } from '../types';

interface DataSearchTarget {
  connId: string;
  connName: string;
  database: string;
}

interface DataSearchDialogProps {
  target: DataSearchTarget;
  onClose: () => void;
}

interface HitRow {
  schema: string;
  table: string;
  hits: number;
  error?: string;
  columns?: Column[];
  rows?: any[][];
}

// "在数据库中搜索数据" (P2-10, the TablePlus Search-in-database): walks the
// keyword through every table's text columns server-side, streaming progress.
// The walk is capped (objects scanned, rows per table) and this dialog owns the
// cancel: closing or clicking 停止 aborts the fetch, which cancels the server
// walk too — a search must never keep scanning after the user left.
const DataSearchDialog: React.FC<DataSearchDialogProps> = ({ target, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);
  const abortRef = useRef<AbortController | null>(null);
  const [keyword, setKeyword] = useState('');
  const [running, setRunning] = useState(false);
  const [scanned, setScanned] = useState(0);
  const [total, setTotal] = useState(0);
  const [hits, setHits] = useState<HitRow[]>([]);
  const [note, setNote] = useState('');

  useEffect(() => () => abortRef.current?.abort(), []);

  const stop = () => {
    abortRef.current?.abort();
    abortRef.current = null;
    setRunning(false);
  };

  const start = async () => {
    const term = keyword.trim();
    if (!term || running) return;
    stop();
    const controller = new AbortController();
    abortRef.current = controller;
    setRunning(true);
    setHits([]);
    setScanned(0);
    setTotal(0);
    setNote('');
    try {
      const final = await SearchDatabaseData(target.connId, target.database, term, {
        signal: controller.signal,
        onStart: tables => setTotal(tables),
        onTable: (event: SearchDataEvent) => {
          setScanned(event.scanned ?? 0);
          if (event.hits || event.error) {
            setHits(current => [
              ...current,
              {
                schema: event.schema ?? '',
                table: event.table ?? '',
                hits: event.hits ?? 0,
                error: event.error,
                columns: event.columns,
                rows: event.rows,
              },
            ]);
          }
        },
      });
      if (final.truncated) setNote('部分对象被跳过：命中的表行数达到上限。');
      if (final.cancelled) setNote('已停止。');
      if (final.error) setNote(final.error);
    } catch (error) {
      if (!controller.signal.aborted) setNote((error as Error).message);
      else setNote('已停止。');
    } finally {
      setRunning(false);
    }
  };

  const hitCount = hits.reduce((sum, h) => sum + (h.error ? 0 : h.hits), 0);
  const [open, setOpen] = useState<string | null>(null);
  const resultOf = (h: HitRow): QueryResult => ({
    columns: h.columns ?? [],
    rows: h.rows ?? [],
    rowCount: h.rows?.length ?? 0,
    affectedRows: 0,
    lastInsertId: 0,
    executionTime: 0,
  });

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="在数据库中搜索数据"
        className="bg-panel rounded-lg w-full max-w-xl max-h-[75vh] shadow-xl flex flex-col"
      >
        <div className="flex items-center justify-between px-5 py-3 border-b border-line">
          <h2 className="text-sm font-semibold">
            搜索数据
            <span className="text-muted font-normal"> · {target.connName}.{target.database}</span>
          </h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭">
            <FiX size={16} />
          </button>
        </div>

        <div className="p-4 space-y-3">
          <div className="flex gap-2">
            <input
              autoFocus
              value={keyword}
              onChange={e => setKeyword(e.target.value)}
              onKeyDown={e => {
                if (e.key === 'Enter') void start();
              }}
              placeholder="要在文本列里查找的内容（区分大小写按数据库规则）"
              aria-label="搜索关键词"
              className="flex-1 bg-canvas border border-line rounded px-2 py-1 text-sm text-ink focus:outline-none focus:border-accent"
            />
            {running ? (
              <button
                className="px-3 py-1 rounded text-2xs border border-line text-secondary hover:text-ink hover:bg-raised"
                onClick={stop}
              >
                停止
              </button>
            ) : (
              <button
                className="flex items-center gap-1 px-3 py-1 rounded text-2xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
                disabled={!keyword.trim()}
                onClick={() => void start()}
              >
                <FiSearch size={12} />
                搜索
              </button>
            )}
          </div>

          {running && (
            <div className="flex items-center gap-2 text-2xs text-secondary">
              <FiLoader className="animate-spin" size={12} />
              <span>正在扫描第 {scanned}/{total} 个对象…</span>
            </div>
          )}
          {!running && note && <div className="text-2xs text-warning whitespace-pre-wrap">{note}</div>}
          {!running && !note && total > 0 && hitCount === 0 && (
            <div className="text-2xs text-muted">扫描完 {total} 个对象，没有命中。</div>
          )}

          {hits.length > 0 && (
            <div className="max-h-[45vh] overflow-auto border border-line rounded">
              <table className="w-full text-xs border-collapse">
                <thead className="sticky top-0 bg-panel">
                  <tr className="border-b border-line">
                    <th className="text-left px-2 py-1 font-medium text-secondary">对象</th>
                    <th className="text-right px-2 py-1 font-medium text-secondary w-20">命中行</th>
                  </tr>
                </thead>
                <tbody>
                  {hits.map(h => {
                    const id = `${h.schema}.${h.table}`;
                    const expanded = open === id && (h.rows?.length ?? 0) > 0;
                    return (
                      <Fragment key={id}>
                        <tr
                          className={`even:bg-row-alt/40 border-b border-line/50 ${
                            (h.rows?.length ?? 0) > 0 && !h.error ? 'cursor-pointer' : ''
                          }`}
                          onClick={() => {
                            if (h.error || !h.rows?.length) return;
                            setOpen(expanded ? null : id);
                          }}
                        >
                          <td className="px-2 py-1 font-mono">
                            {(h.rows?.length ?? 0) > 0 && (
                              <span className="inline-block w-3 align-middle text-muted" aria-hidden>
                                {expanded ? <FiChevronDown size={11} /> : <FiChevronRight size={11} />}
                              </span>
                            )}
                            {h.schema ? `${h.schema}.` : ''}
                            {h.table}
                            {h.error && (
                              <span className="text-danger ml-2 font-sans text-2xs" title={h.error}>
                                跳过：{h.error}
                              </span>
                            )}
                          </td>
                          <td className="px-2 py-1 text-right tabular-nums">{h.error ? '—' : h.hits}</td>
                        </tr>
                        {expanded && (
                          <tr className="bg-canvas">
                            <td colSpan={2} className="p-0">
                              <div className="max-h-[35vh] overflow-auto">
                                <DataGrid result={resultOf(h)} exportName={`search-${target.database}-${id}`} />
                              </div>
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};

export default DataSearchDialog;
