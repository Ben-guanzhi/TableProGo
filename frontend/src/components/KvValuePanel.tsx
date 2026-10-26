import React, { useState } from 'react';
import { FiChevronDown, FiLoader, FiPlus, FiRefreshCw } from 'react-icons/fi';
import toast from 'react-hot-toast';
import DataGrid from './DataGrid';
import ErrorBanner from './ErrorBanner';
import KvAddRowDialog from './KvAddRowDialog';
import KvEditDialog from './KvEditDialog';
import { AddKVRow, SetKVElement } from '../api';
import { kvAddRow } from '../kvAddRow';
import type { KVAddRowDraft } from '../kvAddRow';
import { kvEditTarget } from '../kvEdit';
import type { KVEditTarget } from '../kvEdit';
import { KVValue, SectionState } from '../types';

interface KvValuePanelProps {
  connId: string;
  database: string;
  kv: SectionState<KVValue>;
  onRefresh: () => void;
  onDismissError: () => void;
  // One more window of the value (P3-3). Absent when the read finished or the
  // driver cannot page; the button then does not exist.
  onLoadMore?: () => void;
}

// One Redis key, read in windows when the server says more rows exist. String,
// hash, list and sorted-set cells can be rewritten in place. A set has no
// writable value column and a stream entry is an append-only record, so their
// grid stays read-only and new rows go through the append dialog (SADD/XADD).
const KvValuePanel: React.FC<KvValuePanelProps> = ({
  connId,
  database,
  kv,
  onRefresh,
  onDismissError,
  onLoadMore,
}) => {
  const value = kv.data;
  const appendable = value?.type === 'set' || value?.type === 'stream';
  const [target, setTarget] = useState<KVEditTarget | null>(null);
  const [adding, setAdding] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');

  const beginEdit = (rowIndex: number, column: string) => {
    if (!value) return;
    const next = kvEditTarget(value.type, value.result.rows[rowIndex], column);
    if (!next) return;
    setSaveError('');
    setTarget(next);
  };

  const save = async (text: string) => {
    if (!value || !target) return;
    setSaving(true);
    setSaveError('');
    try {
      await SetKVElement({
        connId,
        database,
        key: value.key,
        identity: target.identity,
        id: target.id,
        column: target.column,
        value: text,
      });
      setTarget(null);
      onRefresh();
    } catch (error) {
      setSaveError((error as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const appendRow = async (draft: KVAddRowDraft) => {
    if (!value || !appendable) return;
    const parsed = kvAddRow(value.type, draft);
    if (parsed.error || !parsed.row) {
      setSaveError(parsed.error ?? '追加内容无效');
      return;
    }
    setSaving(true);
    setSaveError('');
    try {
      const res = await AddKVRow({
        connId,
        database,
        key: value.key,
        kind: value.type as 'set' | 'stream',
        ...parsed.row,
      });
      setAdding(false);
      toast.success(value.type === 'set' ? `已追加成员 ${parsed.row.member}` : `已追加条目 ${res.id}`);
      onRefresh();
    } catch (error) {
      setSaveError((error as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex-1 flex flex-col min-h-0">
      <div className="flex items-center gap-3 px-3 py-1.5 border-b border-line bg-panel shrink-0">
        <span className="text-2xs uppercase tracking-wide text-muted">Key</span>
        <span className="font-mono text-[0.8125rem] truncate max-w-[45%]" title={value?.key}>
          {value?.key ?? '…'}
        </span>
        {value && (
          <span className="px-1.5 py-0.5 rounded bg-raised text-2xs text-secondary border border-line">
            {value.type}
          </span>
        )}
        {value && <span className="text-2xs text-muted">TTL {value.ttl}</span>}
        <span className="ml-auto text-2xs text-muted min-w-0 truncate">
          db {database} · {appendable ? '行只读，可追加' : '双击值可改'}
        </span>
        {appendable && (
          <button
            className="flex items-center gap-1 px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised border border-line shrink-0"
            onClick={() => {
              setSaveError('');
              setAdding(true);
            }}
            title={value?.type === 'set' ? '向集合追加成员' : '向流追加条目'}
          >
            <FiPlus size={12} />
            追加行
          </button>
        )}
        <button
          className="p-1 text-secondary hover:text-ink shrink-0"
          onClick={onRefresh}
          disabled={!!kv.loading}
          title="重新读取键值"
          aria-label="重新读取键值"
        >
          <FiRefreshCw size={13} className={kv.loading ? 'animate-spin' : ''} />
        </button>
      </div>

      {kv.loading && !value && (
        <div className="flex-1 flex items-center justify-center gap-2 text-muted text-sm">
          <FiLoader className="animate-spin" size={16} />
          <span>读取键值…</span>
        </div>
      )}

      {kv.error && <ErrorBanner message={kv.error} onDismiss={onDismissError} />}

      {value && (
        <div className="flex-1 flex flex-col min-h-0">
          <DataGrid
            result={value.result}
            exportName={`redis-${database}-${value.key}`}
            onCellDoubleClick={beginEdit}
          />
          {onLoadMore && (
            <div className="flex items-center justify-center px-3 py-1.5 border-t border-line bg-panel shrink-0">
              <button
                className="flex items-center gap-1 px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised border border-line"
                onClick={onLoadMore}
                disabled={!!kv.loading}
                title="该键还有更多成员；读取下一窗口"
              >
                {kv.loading ? <FiLoader size={12} className="animate-spin" /> : <FiChevronDown size={12} />}
                加载更多值（已显示 {value.result.rowCount} 行）
              </button>
            </div>
          )}
        </div>
      )}

      {target && (
        <KvEditDialog
          key={`${target.identity}|${target.id}|${target.column}`}
          target={target}
          saving={saving}
          error={saveError}
          onSave={save}
          onClose={() => {
            if (!saving) setTarget(null);
          }}
        />
      )}

      {adding && value && appendable && (
        <KvAddRowDialog
          key={value.key}
          type={value.type as 'set' | 'stream'}
          name={value.key}
          saving={saving}
          error={saveError}
          onSubmit={appendRow}
          onEdit={() => setSaveError('')}
          onClose={() => {
            if (!saving) setAdding(false);
          }}
        />
      )}
    </div>
  );
};

export default KvValuePanel;
