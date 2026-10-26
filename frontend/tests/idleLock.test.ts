import assert from 'node:assert/strict';
import { test } from 'node:test';

import { decideLock, lockMessage, SLEEP_GAP_MS } from '../src/lib/idleLock.ts';
import type { IdleSample } from '../src/lib/idleLock.ts';

const base = (over: Partial<IdleSample> = {}): IdleSample => ({
  now: 1_000_000,
  lastTick: 970_000,
  lastActivity: 990_000,
  systemIdleSeconds: -1,
  hasPasscode: true,
  idleLockMinutes: 5,
  lockOnHide: false,
  lockOnSleep: true,
  hidden: false,
  ...over,
});

test('no passcode means no lock, whatever else says', () => {
  // A lock screen nothing can unlock is a dead end, so the passcode gates
  // every rule - including sleep.
  assert.deepEqual(decideLock(base({ hasPasscode: false, idleLockMinutes: 1, lastActivity: 0 })), {
    lock: false,
  });
  assert.deepEqual(decideLock(base({ hasPasscode: false, lastTick: 0, now: 10_000_000 })), {
    lock: false,
  });
});

test('page activity is the fallback only when the OS reports nothing', () => {
  const longAgo = 1_000_000 - 6 * 60_000;
  assert.deepEqual(decideLock(base({ lastActivity: longAgo, systemIdleSeconds: -1 })), {
    lock: true,
    reason: 'page',
  });
  // A platform that reports idle wins over the page clock, even when the page
  // looks stale: a backgrounded window stops firing events without the user
  // being away.
  assert.deepEqual(decideLock(base({ lastActivity: longAgo, systemIdleSeconds: 10 })), {
    lock: false,
  });
  assert.deepEqual(decideLock(base({ lastActivity: 999_000, systemIdleSeconds: 6 * 60 })), {
    lock: true,
    reason: 'system',
  });
});

test('idleLockMinutes=0 disables the idle rules but not sleep', () => {
  assert.deepEqual(decideLock(base({ idleLockMinutes: 0, lastActivity: 0, systemIdleSeconds: 1e9 })), {
    lock: false,
  });
  assert.deepEqual(decideLock(base({ idleLockMinutes: 0, lastTick: 0, systemIdleSeconds: 1e9 })), {
    lock: true,
    reason: 'sleep',
  });
});

test('a wall-clock jump past the tick means the process was frozen', () => {
  const sample = base({ now: base().now + SLEEP_GAP_MS + 1, lastTick: base().now });
  assert.deepEqual(decideLock(sample), { lock: true, reason: 'sleep' });
  // Exactly at the margin is still a loaded machine, not a suspend.
  assert.deepEqual(decideLock(base({ now: base().now + SLEEP_GAP_MS, lastTick: base().now })), {
    lock: false,
  });
  assert.deepEqual(decideLock(base({ lockOnSleep: false, lastTick: 0 })), { lock: false });
  // The first tick has no previous sample to compare against.
  assert.deepEqual(decideLock(base({ lastTick: null })), { lock: false });
});

test('hiding the window locks only when asked to', () => {
  assert.deepEqual(decideLock(base({ hidden: true, lockOnHide: true })), { lock: true, reason: 'hide' });
  assert.deepEqual(decideLock(base({ hidden: true, lockOnHide: false })), { lock: false });
  assert.deepEqual(decideLock(base({ hidden: true, lockOnHide: true, idleLockMinutes: 0 })), {
    lock: true,
    reason: 'hide',
  });
});

test('sleep outranks hide, hide outranks idle', () => {
  const all = base({ lastTick: 0, hidden: true, lockOnHide: true, lastActivity: 0, systemIdleSeconds: 1e9 });
  assert.deepEqual(decideLock(all), { lock: true, reason: 'sleep' });
  const hideOverIdle = base({
    hidden: true,
    lockOnHide: true,
    lastActivity: 0,
    systemIdleSeconds: 1e9,
  });
  assert.deepEqual(decideLock(hideOverIdle), { lock: true, reason: 'hide' });
});

test('the message names what was detected, not what was done', () => {
  assert.match(lockMessage('system', 5), /系统空闲 5 分钟/);
  assert.match(lockMessage('page', 5), /空闲 5 分钟/);
  assert.match(lockMessage('sleep', 5), /休眠/);
  assert.match(lockMessage('hide', 5), /窗口隐藏/);
  assert.match(lockMessage('startup', 5), /应用已锁定/);
});
