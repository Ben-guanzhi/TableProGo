import React, { useEffect, useRef, useState } from 'react';
import { FiX } from 'react-icons/fi';

import { ClearPasscode, SetPasscode, VerifyPasscode } from '../api';
import { useDialog } from '../dialog';

interface PasscodeDialogProps {
  passcodeSet: boolean;
  onClose: () => void;
  // The lock watcher caches "is there a passcode"; it must re-read after a
  // set/clear or the idle rules would keep using the old answer.
  onChanged: () => void;
}

// Managing the local passcode (P2-9). Changing or removing an existing one
// requires typing it first: the dialog is the only place that can disarm the
// idle lock, so it must not be reachable by whoever happens to be at an
// unlocked screen — that is the whole point of a lock.
const PasscodeDialog: React.FC<PasscodeDialogProps> = ({ passcodeSet, onClose, onChanged }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    panelRef.current?.querySelector<HTMLInputElement>('input')?.focus();
  }, []);

  const save = async () => {
    setError('');
    if (passcodeSet) {
      if (!current) {
        setError('请输入当前口令');
        return;
      }
      const ok = await VerifyPasscode(current);
      if (!ok) {
        setError('当前口令错误');
        return;
      }
    }
    if (next.length < 4) {
      setError('新口令至少 4 位');
      return;
    }
    if (next !== confirm) {
      setError('两次输入的新口令不一致');
      return;
    }
    setBusy(true);
    try {
      await SetPasscode(next);
      onChanged();
      onClose();
    } catch (err) {
      setError((err as Error).message || '保存失败');
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setError('');
    if (!current) {
      setError('移除口令前请输入当前口令');
      return;
    }
    const ok = await VerifyPasscode(current);
    if (!ok) {
      setError('当前口令错误');
      return;
    }
    setBusy(true);
    try {
      await ClearPasscode();
      onChanged();
      onClose();
    } catch (err) {
      setError((err as Error).message || '移除失败');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-scrim/50 flex items-center justify-center z-50">
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="本地口令"
        className="bg-panel rounded-lg w-full max-w-sm shadow-xl"
      >
        <div className="flex items-center justify-between px-5 py-3 border-b border-line">
          <h2 className="text-sm font-semibold">{passcodeSet ? '修改本地口令' : '设置本地口令'}</h2>
          <button className="text-secondary hover:text-ink" onClick={onClose} aria-label="关闭">
            <FiX size={16} />
          </button>
        </div>
        <form
          className="p-4 space-y-2"
          onSubmit={e => {
            e.preventDefault();
            void save();
          }}
        >
          <p className="text-2xs text-muted">
            口令只以 argon2id 哈希保存在本机，用于空闲自动锁定后解锁；忘记口令只能删除
            passcode.json 重新设置。
          </p>
          {passcodeSet && (
            <label className="block text-xs text-ink">
              当前口令
              <input
                type="password"
                value={current}
                onChange={e => setCurrent(e.target.value)}
                autoComplete="current-password"
                className="mt-1 w-full bg-canvas border border-line-strong rounded px-2 py-1 text-sm text-ink focus:outline-none focus:border-accent"
              />
            </label>
          )}
          <label className="block text-xs text-ink">
            新口令（至少 4 位）
            <input
              type="password"
              value={next}
              onChange={e => setNext(e.target.value)}
              autoComplete="new-password"
              className="mt-1 w-full bg-canvas border border-line-strong rounded px-2 py-1 text-sm text-ink focus:outline-none focus:border-accent"
            />
          </label>
          <label className="block text-xs text-ink">
            再输一次
            <input
              type="password"
              value={confirm}
              onChange={e => setConfirm(e.target.value)}
              autoComplete="new-password"
              className="mt-1 w-full bg-canvas border border-line-strong rounded px-2 py-1 text-sm text-ink focus:outline-none focus:border-accent"
            />
          </label>
          {error && (
            <div role="alert" className="text-2xs text-danger">
              {error}
            </div>
          )}
          <div className="flex items-center justify-between gap-2 pt-1">
            {passcodeSet ? (
              <button
                type="button"
                disabled={busy}
                onClick={() => void remove()}
                className="px-2 py-1 rounded text-2xs text-danger border border-line hover:bg-raised disabled:opacity-40"
              >
                移除口令
              </button>
            ) : (
              <span />
            )}
            <div className="flex gap-2">
              <button
                type="button"
                onClick={onClose}
                className="px-3 py-1 rounded text-2xs border border-line text-secondary hover:text-ink hover:bg-raised"
              >
                取消
              </button>
              <button
                type="submit"
                disabled={busy}
                className="px-3 py-1 rounded text-2xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
              >
                {busy ? '保存中…' : '保存'}
              </button>
            </div>
          </div>
        </form>
      </div>
    </div>
  );
};

export default PasscodeDialog;
