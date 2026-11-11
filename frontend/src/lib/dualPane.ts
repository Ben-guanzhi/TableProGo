// W2-a: the dual-pane editor's state rules, kept out of the component so the
// "which pane runs" / "which pane is active" questions are testable without
// mounting CodeMirror.
//
// The model is deliberately small: a tab holds two independent statements
// (pane 1 and pane 2) and one `activePane` marker. The result grid is shared —
// there is only ever one result on screen, from whichever pane last ran. That
// is what makes the feature cheap: no second result model, no second write-back
// source, no second loading flag.

import type { Tab } from '../types/index';

export type PaneId = 1 | 2;

// dualPaneOf reports whether the tab shows two editors. A tab that never
// toggled the mode is single-pane, which is every tab before W2-a.
export const dualPaneOf = (tab: Pick<Tab, 'dualPane'>): boolean => !!tab.dualPane;

// activePaneOf returns the pane whose statement a run acts on. A single-pane
// tab always answers 1, so callers need no branch for the old shape.
export const activePaneOf = (tab: Pick<Tab, 'dualPane' | 'activePane'>): PaneId =>
  tab.dualPane && tab.activePane === 2 ? 2 : 1;

// paneSQL reads one pane's statement. Pane 1 is the tab's historical `query`,
// so a tab that never opened the second pane reads exactly as before.
export const paneSQL = (tab: Pick<Tab, 'query' | 'query2'>, pane: PaneId): string =>
  pane === 2 ? tab.query2 ?? '' : tab.query;

// withPaneSQL writes one pane's statement, leaving the other untouched. It is
// the single place that knows pane 2's field name, so a future third pane
// changes one file instead of every call site.
export const withPaneSQL = (tab: Tab, pane: PaneId, sql: string): Tab =>
  pane === 2 ? { ...tab, query2: sql, isDirty: true } : { ...tab, query: sql, isDirty: true };

// PaneTarget names what a run should execute: which statement, and which pane
// it came from (the latter so the editor can mark the right pane as running).
export interface PaneTarget {
  pane: PaneId;
  sql: string;
}

// runTarget resolves a run request against a tab. It returns null when the
// active pane is empty — running an empty statement would only produce a
// server-side syntax error, and the pane that is blank is not the one the user
// meant to run.
//
// override (the editor's own text) wins over the stored tab state for the pane
// it came from, exactly as the single-pane path already does: a keystroke and
// the Ctrl+Enter that follows land in the same React batch, so the tab state is
// still the pre-keystroke text.
export const runTarget = (
  tab: Pick<Tab, 'query' | 'query2' | 'dualPane' | 'activePane'>,
  override?: string,
): PaneTarget | null => {
  const pane = activePaneOf(tab);
  const sql = override ?? paneSQL(tab, pane);
  const trimmed = sql.trim();
  if (!trimmed) return null;
  return { pane, sql };
};

// toggleDualPane flips the mode and keeps the invariant that a single-pane tab
// is always on pane 1: leaving dual mode while on pane 2 would otherwise leave
// a tab whose active pane does not exist.
export const toggleDualPane = (tab: Tab): Tab =>
  tab.dualPane
    ? { ...tab, dualPane: false, activePane: 1 }
    : { ...tab, dualPane: true };

// setActivePane marks which pane a run would act on. On a single-pane tab the
// request is honoured only for pane 1 — there is no second pane to focus.
export const setActivePane = (tab: Tab, pane: PaneId): Tab =>
  tab.dualPane ? { ...tab, activePane: pane } : { ...tab, activePane: 1 };
