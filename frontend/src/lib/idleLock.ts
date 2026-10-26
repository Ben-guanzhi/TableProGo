// P2-9 idle-lock decision, kept pure so the rules are testable without a DOM.
//
// The watcher samples every 30s. Each sample decides whether to lock, and how
// the lock was earned — the reason matters because the message the user sees
// after unlocking should say why ("you were idle", "the machine slept"), and
// because the three signals have different confidence:
//
//   system  — the OS says nobody touched keyboard/mouse for that long. Strong:
//             it keeps growing while the screen is locked, which page events
//             cannot see. Only Windows reports it; elsewhere it is -1.
//   page    — no mousemove/keydown in this window for that long. Weaker: a
//             backgrounded or locked window stops firing events too, so this
//             can only be used when the OS gives nothing.
//   sleep   — the wall clock jumped far past the tick interval, which means
//             the process was frozen (suspend/hibernate). A woken machine is
//             exactly the case a user walking away looks like to a timer, so
//             this locks even when idle minutes are set to 0.
//
// No passcode means no lock: a lock screen nothing can unlock is a dead end.

export type LockReason = 'system' | 'page' | 'sleep' | 'hide' | 'startup';

export interface IdleSample {
  /** Wall clock now, ms. */
  now: number;
  /** Wall clock of the previous sample, ms (null before the first tick). */
  lastTick: number | null;
  /** Wall clock of the last in-page mousemove/keydown, ms. */
  lastActivity: number;
  /** OS-reported idle seconds, or -1 when the platform does not report. */
  systemIdleSeconds: number;
  hasPasscode: boolean;
  idleLockMinutes: number;
  lockOnHide: boolean;
  lockOnSleep: boolean;
  /** The window is hidden/backgrounded right now (visibilitychange). */
  hidden: boolean;
}

export type LockDecision = { lock: false } | { lock: true; reason: LockReason };

// A tick gap beyond this means the process was frozen, not merely unscheduled:
// the timer fires every 30s, so 3x that is generous for a loaded machine and
// catches any real suspend (db-dev uses the same 90s margin).
export const SLEEP_GAP_MS = 90_000;

export const decideLock = (s: IdleSample): LockDecision => {
  if (!s.hasPasscode) return { lock: false };

  // Sleep first: a frozen process has no trustworthy idea of "idle" at all,
  // and this is the one rule that fires even with idle locking turned off.
  if (s.lockOnSleep && s.lastTick !== null && s.now - s.lastTick > SLEEP_GAP_MS) {
    return { lock: true, reason: 'sleep' };
  }

  if (s.hidden && s.lockOnHide) return { lock: true, reason: 'hide' };

  const mins = s.idleLockMinutes;
  if (mins <= 0) return { lock: false };
  const limitMs = mins * 60_000;

  if (s.systemIdleSeconds >= 0) {
    return s.systemIdleSeconds * 1000 >= limitMs ? { lock: true, reason: 'system' } : { lock: false };
  }
  return s.now - s.lastActivity >= limitMs ? { lock: true, reason: 'page' } : { lock: false };
};

// What the lock screen says after the user returns. The reason is phrased the
// way db-dev phrases it: tell the user what was detected, not what was done.
export const lockMessage = (reason: LockReason, minutes: number): string => {
  switch (reason) {
    case 'system':
      return `系统空闲 ${minutes} 分钟，已锁定并断开连接`;
    case 'page':
      return `空闲 ${minutes} 分钟，已锁定并断开连接`;
    case 'sleep':
      return '检测到系统休眠，已锁定并断开连接';
    case 'hide':
      return '窗口隐藏，已锁定并断开连接';
    case 'startup':
      return '应用已锁定，输入口令继续';
  }
};
