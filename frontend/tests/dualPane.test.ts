import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  activePaneOf,
  dualPaneOf,
  paneSQL,
  runTarget,
  setActivePane,
  toggleDualPane,
  withPaneSQL,
} from '../src/lib/dualPane.ts';
import type { Tab } from '../src/types/index.ts';

const tab = (over: Partial<Tab> = {}): Tab =>
  ({ id: 't1', title: '查询', query: 'SELECT 1', isDirty: false, ...over }) as Tab;

// ---- mode ----

test('a tab that never toggled dual mode is single-pane on pane 1', () => {
  const t = tab();
  assert.equal(dualPaneOf(t), false);
  assert.equal(activePaneOf(t), 1);
  assert.equal(paneSQL(t, 1), 'SELECT 1');
  assert.equal(paneSQL(t, 2), '', 'an unopened pane 2 reads empty, not undefined');
});

test('toggling dual mode keeps a single-pane tab on pane 1', () => {
  const on = toggleDualPane(tab());
  assert.equal(on.dualPane, true);
  assert.equal(on.activePane, undefined, 'opening the pane does not itself pick one');

  const off = toggleDualPane(tab({ dualPane: true, activePane: 2 }));
  assert.equal(off.dualPane, false);
  assert.equal(off.activePane, 1, 'leaving dual mode must not leave a dangling pane 2');
});

test('activePaneOf honours pane 2 only in dual mode', () => {
  assert.equal(activePaneOf(tab({ dualPane: true, activePane: 2 })), 2);
  assert.equal(activePaneOf(tab({ dualPane: true, activePane: 1 })), 1);
  // A single-pane tab cannot target a pane that is not shown.
  assert.equal(activePaneOf(tab({ activePane: 2 })), 1);
});

test('setActivePane is a no-op on a single-pane tab', () => {
  assert.equal(setActivePane(tab(), 2).activePane, 1);
  assert.equal(setActivePane(tab({ dualPane: true }), 2).activePane, 2);
});

// ---- statement routing ----

test('paneSQL reads each pane independently', () => {
  const t = tab({ dualPane: true, query: 'SELECT a', query2: 'SELECT b' });
  assert.equal(paneSQL(t, 1), 'SELECT a');
  assert.equal(paneSQL(t, 2), 'SELECT b');
});

test('withPaneSQL writes one pane and leaves the other alone', () => {
  const t = tab({ dualPane: true, query: 'SELECT a', query2: 'SELECT b' });
  const next = withPaneSQL(t, 2, 'SELECT c');
  assert.equal(next.query, 'SELECT a');
  assert.equal(next.query2, 'SELECT c');
  assert.equal(next.isDirty, true);

  const first = withPaneSQL(t, 1, 'SELECT z');
  assert.equal(first.query, 'SELECT z');
  assert.equal(first.query2, 'SELECT b', 'editing pane 1 must not clear pane 2');
});

// ---- run target ----

test('a run takes the active pane, and an override wins for it', () => {
  const t = tab({ dualPane: true, activePane: 2, query: 'SELECT a', query2: 'SELECT b' });
  assert.deepEqual(runTarget(t), { pane: 2, sql: 'SELECT b' });
  // The override is the pane's own live text (a keystroke and the Ctrl+Enter
  // land in one React batch, so the stored tab state is stale).
  assert.deepEqual(runTarget(t, 'SELECT live'), { pane: 2, sql: 'SELECT live' });

  const first = tab({ dualPane: true, activePane: 1, query: 'SELECT a', query2: 'SELECT b' });
  assert.deepEqual(runTarget(first), { pane: 1, sql: 'SELECT a' });
});

test('an empty active pane refuses to run instead of sending blank SQL', () => {
  const t = tab({ dualPane: true, activePane: 2, query: 'SELECT a', query2: '   ' });
  assert.equal(runTarget(t), null);
  // A whitespace-only statement is empty: the server would only answer with a
  // syntax error and the user would blame the other pane's text.
  assert.equal(runTarget(tab({ query: '\n\t ' })), null);
});

// ---- invariants ----

test('the active pane always names a pane that is actually mounted', () => {
  // Property-ish check over the reachable mode/active combinations: a single
  // pane is always 1, and dual mode is either 1 or 2 — never the other pane.
  for (const dualPane of [false, true]) {
    for (const activePane of [1, 2] as const) {
      for (const stored of [undefined, 1, 2] as const) {
        const t = tab({ dualPane, activePane: stored });
        const pane = activePaneOf(t);
        assert.ok(pane === 1 || (dualPane && pane === 2),
          `dual=${dualPane} stored=${stored}: pane ${pane} is not mounted`);
        assert.equal(activePaneOf(t), dualPane && stored === 2 ? 2 : 1);
        assert.notEqual(pane, activePane === 2 && !dualPane ? 2 : 0);
      }
    }
  }
});
