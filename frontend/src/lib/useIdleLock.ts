import { useCallback, useEffect, useRef, useState } from 'react';

import { HasPasscode, SystemIdleSeconds, VerifyPasscode } from '../api';
import type { AppSettings } from '../api';
import { decideLock } from './idleLock';
import type { LockReason } from './idleLock';

// The idle-lock watcher (P2-9). One interval every 30s decides through the
// pure decideLock rules whether to lock; mousemove/keydown reset the in-page
// activity clock; visibilitychange feeds the hide rule and the sleep gap.
//
// Locked state lives here rather than in App because the watcher owns every
// signal: a lock it triggers must also be the one that can end. The frontend
// keeps no passcode; unlocking calls the backend's VerifyPasscode.

export interface IdleLockState {
  locked: boolean;
  reason: LockReason | null;
  /** True once the backend reports a passcode is stored. */
  passcodeSet: boolean;
  lock: (reason: LockReason) => void;
  /** Verify against the stored passcode; unlocks on success. */
  unlock: (passcode: string) => Promise<boolean>;
  /** Re-read whether a passcode exists, after set/clear from the UI. */
  refreshPasscode: () => Promise<void>;
}

export function useIdleLock(
  settings: AppSettings,
  onLocked: () => void,
): IdleLockState {
  const [locked, setLocked] = useState(false);
  const [reason, setReason] = useState<LockReason | null>(null);
  const [passcodeSet, setPasscodeSet] = useState(false);

  const lastActivity = useRef(Date.now());
  const lastTick = useRef<number | null>(null);
  const startupChecked = useRef(false);
  const hidden = useRef(typeof document !== 'undefined' && document.hidden);
  const settingsRef = useRef(settings);
  settingsRef.current = settings;
  const onLockedRef = useRef(onLocked);
  onLockedRef.current = onLocked;
  const passcodeRef = useRef(false);

  const refreshPasscode = useCallback(async () => {
    try {
      const has = await HasPasscode();
      passcodeRef.current = has;
      setPasscodeSet(has);
      // A passcode stored on disk means the app opens locked, every launch
      // (db-dev does the same): the point of the passcode is that a restart
      // does not hand the data to whoever walks up to the machine. Only the
      // first read locks — setting a passcode mid-session must not.
      if (has && !startupChecked.current) {
        startupChecked.current = true;
        setLocked(true);
        setReason('startup');
        // Anything the page reconnected before this answer arrived is closed
        // again: a startup lock must leave no live session behind.
        onLockedRef.current();
      }
      return;
    } catch {
      // A backend that cannot answer keeps the previous answer; guessing
      // "no passcode" every failed poll would silently disarm the lock.
    }
  }, []);

  const lock = useCallback((why: LockReason) => {
    setLocked(true);
    setReason(why);
    // The lock disconnects everything behind it (the caller's onLocked does
    // the API call); re-arm the activity clock so unlocking does not instantly
    // re-lock on the stale timer gap.
    lastActivity.current = Date.now();
    lastTick.current = Date.now();
  }, []);

  useEffect(() => {
    void refreshPasscode();
  }, [refreshPasscode]);

  useEffect(() => {
    const reset = () => {
      lastActivity.current = Date.now();
    };
    const onVisibility = () => {
      hidden.current = document.hidden;
      if (!document.hidden) {
        // Returning to a visible window is a fresh start for the page clock;
        // the hide rule already fired (or declined) at the moment of hiding.
        reset();
        return;
      }
      // Hiding locks immediately when asked to: the 30s poll is far too late
      // for "window hidden" to feel like a lock.
      const s = settingsRef.current;
      if (passcodeRef.current && s.lockOnHide) {
        lock('hide');
        onLockedRef.current();
      }
    };
    window.addEventListener('mousemove', reset, { passive: true });
    window.addEventListener('keydown', reset, { passive: true });
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      window.removeEventListener('mousemove', reset);
      window.removeEventListener('keydown', reset);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [lock]);

  useEffect(() => {
    const tick = async () => {
      const now = Date.now();
      const s = settingsRef.current;
      let systemIdle = -1;
      try {
        systemIdle = await SystemIdleSeconds();
      } catch {
        systemIdle = -1;
      }
      const decision = decideLock({
        now,
        lastTick: lastTick.current,
        lastActivity: lastActivity.current,
        systemIdleSeconds: systemIdle,
        hasPasscode: passcodeRef.current,
        idleLockMinutes: s.idleLockMinutes,
        lockOnHide: s.lockOnHide,
        lockOnSleep: s.lockOnSleep,
        hidden: hidden.current,
      });
      lastTick.current = now;
      if (decision.lock) {
        lock(decision.reason);
        onLockedRef.current();
      }
    };
    lastTick.current = Date.now();
    const timer = window.setInterval(() => void tick(), 30_000);
    return () => window.clearInterval(timer);
  }, [lock]);

  const unlock = useCallback(async (passcode: string) => {
    const ok = await VerifyPasscode(passcode);
    if (ok) {
      setLocked(false);
      setReason(null);
      lastActivity.current = Date.now();
      lastTick.current = Date.now();
    }
    return ok;
  }, []);

  return { locked, reason, passcodeSet, lock, unlock, refreshPasscode };
}
