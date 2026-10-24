import React, { useRef, useState } from 'react';
import { useDialog } from '../dialog';
import type { KVEditTarget } from '../kvEdit';

interface Props {
  target: KVEditTarget;
  saving: boolean;
  error: string;
  onSave: (value: string) => void;
  onClose: () => void;
}

// One element overwritten through dbcore.ElementWriter. Unlike a SQL grid this
// writes immediately: there is no transaction to review, and the element's
// identity already names exactly one cell.
const KvEditDialog: React.FC<Props> = ({ target, saving, error, onSave, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  const [draft, setDraft] = useState(target.current);
  useDialog(panelRef, onClose);

  const submit = () => {
    if (saving) return;
    onSave(draft);
  };

  return (
    <div
      className="fixed inset-0 z-[60] bg-scrim/50 flex items-center justify-center px-6"
      onClick={onClose}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="kv-edit-title"
        className="w-full max-w-md bg-panel border border-line-strong rounded-lg shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <h2 id="kv-edit-title" className="px-5 pt-4 text-sm font-semibold">
          编辑 {target.label}
        </h2>
        <p className="px-5 pt-1 text-2xs text-muted">
          {target.identity === 'member' ? '分数按 Redis 的双精度浮点写入。' : '写入后立即生效，键的 TTL 保持不变。'}
        </p>

        <form
          className="px-5 py-3"
          onSubmit={e => {
            e.preventDefault();
            submit();
          }}
        >
          <label htmlFor="kv-edit-value" className="block text-2xs text-secondary mb-1">
            {target.column === 'score' ? '分数' : '值'}
          </label>
          {target.column === 'score' ? (
            <input
              id="kv-edit-value"
              autoFocus
              inputMode="decimal"
              value={draft}
              onChange={e => setDraft(e.target.value)}
              className="w-full bg-canvas border border-accent rounded px-2 py-1 font-mono text-[0.8125rem] text-ink focus:outline-none"
            />
          ) : (
            <textarea
              id="kv-edit-value"
              autoFocus
              rows={5}
              value={draft}
              onChange={e => setDraft(e.target.value)}
              className="w-full resize-y bg-canvas border border-accent rounded px-2 py-1 font-mono text-[0.8125rem] text-ink focus:outline-none"
            />
          )}

          {error && (
            <div role="alert" className="mt-2 text-2xs text-danger whitespace-pre-wrap break-all">
              {error}
            </div>
          )}

          <div className="flex justify-end gap-2 mt-3">
            <button
              type="button"
              className="px-3 py-1 rounded text-[0.75rem] text-secondary hover:text-ink hover:bg-raised"
              onClick={onClose}
            >
              取消
            </button>
            <button
              type="submit"
              disabled={saving}
              className="px-3 py-1 rounded text-[0.75rem] bg-accent hover:bg-accent-hover text-on-accent disabled:opacity-40"
            >
              {saving ? '写入中…' : '写入'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
};

export default KvEditDialog;
