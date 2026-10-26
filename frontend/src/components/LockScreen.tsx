import React, { useEffect, useRef, useState } from 'react';
import { FiLock } from 'react-icons/fi';

import type { LockReason } from '../lib/idleLock';
import { lockMessage } from '../lib/idleLock';

interface LockScreenProps {
  reason: LockReason | null;
  idleLockMinutes: number;
  // Verified against the stored passcode server-side; true means unlocked.
  onUnlock: (passcode: string) => Promise<boolean>;
}

// The idle-lock screen (P2-9). It covers the whole window and cannot be
// dismissed by Escape or by clicking outside: everything under it is database
// content the user chose to guard, and the walk-away detection that raised it
// may have been right.
const LockScreen: React.FC<LockScreenProps> = ({ reason, idleLockMinutes, onUnlock }) => {
  const [value, setValue] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    // Focus is the whole UX of a lock screen: arriving back at the machine
    // should mean typing, not hunting for the field.
    inputRef.current?.focus();
  }, []);

  const submit = async () => {
    if (busy || !value) return;
    setBusy(true);
    try {
      const ok = await onUnlock(value);
      if (!ok) {
        setError('口令错误');
        setValue('');
        inputRef.current?.focus();
      }
    } catch (err) {
      setError((err as Error).message || '校验失败');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="已锁定"
      className="fixed inset-0 z-[100] bg-canvas flex items-center justify-center"
    >
      <div className="w-full max-w-sm px-8 py-10 rounded-lg bg-panel border border-line shadow-2xl flex flex-col items-center gap-4">
        <span className="w-11 h-11 rounded-full bg-raised border border-line flex items-center justify-center text-secondary">
          <FiLock size={18} aria-hidden />
        </span>
        <div className="text-sm font-semibold text-ink">应用已锁定</div>
        {reason && (
          <div className="text-2xs text-muted text-center">
            {lockMessage(reason, idleLockMinutes)}
          </div>
        )}
        <form
          className="w-full flex flex-col gap-2"
          onSubmit={e => {
            e.preventDefault();
            void submit();
          }}
        >
          <input
            ref={inputRef}
            type="password"
            autoFocus
            value={value}
            onChange={e => {
              setValue(e.target.value);
              if (error) setError('');
            }}
            placeholder="输入本地口令解锁"
            aria-label="本地口令"
            aria-invalid={error ? 'true' : undefined}
            className="w-full bg-canvas border border-line-strong rounded px-3 py-2 text-sm text-ink text-center focus:outline-none focus:border-accent"
          />
          {error && (
            <div role="alert" className="text-2xs text-danger text-center">
              {error}
            </div>
          )}
          <button
            type="submit"
            disabled={!value || busy}
            className="w-full px-3 py-2 rounded text-xs bg-accent text-on-accent hover:bg-accent-hover disabled:opacity-40"
          >
            {busy ? '校验中…' : '解锁'}
          </button>
        </form>
      </div>
    </div>
  );
};

export default LockScreen;
