import React, { useRef, useState } from 'react';
import { FiPlus, FiX } from 'react-icons/fi';
import { useDialog } from '../dialog';
import type { KVAddRowDraft, KVFieldDraft } from '../kvAddRow';

interface Props {
  type: 'set' | 'stream';
  name: string;
  saving: boolean;
  error: string;
  onSubmit: (draft: KVAddRowDraft) => void;
  onEdit: () => void;
  onClose: () => void;
}

// A set member and a stream entry are new rows, not a cell to overwrite, so this
// dialog collects their identity instead of replacing a current value. The
// validation lives in kvAddRow; the error shown here comes back through `error`.
const KvAddRowDialog: React.FC<Props> = ({ type, name, saving, error, onSubmit, onEdit, onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  const [member, setMember] = useState('');
  const [id, setId] = useState('');
  const [fields, setFields] = useState<KVFieldDraft[]>([{ name: '', value: '' }]);
  useDialog(panelRef, onClose);

  const updateField = (index: number, patch: Partial<KVFieldDraft>) => {
    onEdit();
    setFields(prev => prev.map((field, i) => (i === index ? { ...field, ...patch } : field)));
  };

  const submit = () => {
    if (saving) return;
    onSubmit({ member, id, fields });
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
        aria-labelledby="kv-add-row-title"
        className="w-full max-w-lg bg-panel border border-line-strong rounded-lg shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <h2 id="kv-add-row-title" className="px-5 pt-4 text-sm font-semibold">
          向 {name} 追加{type === 'set' ? '成员' : '条目'}
        </h2>
        <p className="px-5 pt-1 text-2xs text-muted">
          {type === 'set'
            ? 'SADD 追加一个成员；成员已存在时不会重复。键的 TTL 保持不变。'
            : 'XADD 追加一条日志；不带 ID 时由服务器生成。键的 TTL 保持不变。'}
        </p>

        <form
          className="px-5 py-3"
          onSubmit={e => {
            e.preventDefault();
            submit();
          }}
        >
          {type === 'set' ? (
            <>
              <label htmlFor="kv-add-row-member" className="block text-2xs text-secondary mb-1">
                成员
              </label>
              <input
                id="kv-add-row-member"
                autoFocus
                value={member}
                onChange={e => {
                  onEdit();
                  setMember(e.target.value);
                }}
                className="w-full bg-canvas border border-accent rounded px-2 py-1 font-mono text-[0.8125rem] text-ink focus:outline-none"
              />
            </>
          ) : (
            <>
              <label htmlFor="kv-add-row-id" className="block text-2xs text-secondary mb-1">
                条目 ID
              </label>
              <input
                id="kv-add-row-id"
                autoFocus
                value={id}
                onChange={e => {
                  onEdit();
                  setId(e.target.value);
                }}
                placeholder="留空或 * 由服务器生成"
                className="w-full bg-canvas border border-accent rounded px-2 py-1 font-mono text-[0.8125rem] text-ink focus:outline-none"
              />

              <span className="block text-2xs text-secondary mt-3 mb-1">字段</span>
              <div className="space-y-1.5">
                {fields.map((field, index) => (
                  <div key={index} className="flex items-center gap-1.5">
                    <input
                      aria-label={`字段 ${index + 1} 名称`}
                      value={field.name}
                      onChange={e => updateField(index, { name: e.target.value })}
                      placeholder="名称"
                      className="w-2/5 bg-canvas border border-line rounded px-2 py-1 font-mono text-[0.8125rem] text-ink focus:outline-none focus:border-accent"
                    />
                    <input
                      aria-label={`字段 ${index + 1} 值`}
                      value={field.value}
                      onChange={e => updateField(index, { value: e.target.value })}
                      placeholder="值"
                      className="flex-1 bg-canvas border border-line rounded px-2 py-1 font-mono text-[0.8125rem] text-ink focus:outline-none focus:border-accent"
                    />
                    <button
                      type="button"
                      className="p-1 text-muted hover:text-danger shrink-0 disabled:opacity-30"
                      onClick={() => {
                        onEdit();
                        setFields(prev => prev.filter((_, i) => i !== index));
                      }}
                      disabled={fields.length === 1}
                      title="删除这一行"
                      aria-label={`删除字段 ${index + 1}`}
                    >
                      <FiX size={13} />
                    </button>
                  </div>
                ))}
              </div>
              <button
                type="button"
                className="flex items-center gap-1 mt-2 px-2 py-0.5 rounded text-2xs text-secondary hover:text-ink hover:bg-raised"
                onClick={() => {
                  onEdit();
                  setFields(prev => [...prev, { name: '', value: '' }]);
                }}
              >
                <FiPlus size={12} /> 添加字段
              </button>
            </>
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
              {saving ? '追加中…' : '追加'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
};

export default KvAddRowDialog;
