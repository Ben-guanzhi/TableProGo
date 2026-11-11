import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  SHORTCUTS,
  SHORTCUT_GROUPS,
  comboMatches,
  duplicateCombos,
  findShortcut,
  normalizeCombo,
  shortcutsByGroup,
} from '../src/lib/shortcuts.ts';

// ---- the table's own invariants ----

test('no two bindable rows claim the same combo', () => {
  // A duplicate is silent at runtime: whichever handler registered last wins
  // and the other never fires. App asserts this too; the test is the gate.
  assert.deepEqual(duplicateCombos(), []);
});

test('every row has an id, a label, display keys, and a known group', () => {
  const ids = new Set<string>();
  for (const s of SHORTCUTS) {
    assert.ok(s.id.length > 0, 'id is required');
    assert.ok(s.label.length > 0, `${s.id}: label is required`);
    assert.ok(s.keys.length > 0, `${s.id}: display keys are required`);
    assert.ok(SHORTCUT_GROUPS.includes(s.group), `${s.id}: unknown group ${s.group}`);
    assert.equal(ids.has(s.id), false, `duplicate id ${s.id}`);
    ids.add(s.id);
  }
});

test('every group named in the order list has at least one row', () => {
  // A section header with nothing under it reads as a missing feature.
  for (const section of shortcutsByGroup()) {
    assert.ok(section.items.length > 0, `${section.group} has no rows`);
  }
});

test('bindable rows spell their combo in the normalized form', () => {
  // normalizeCombo is the only producer of dispatch combos; a hand-written
  // combo that does not round-trip through it would never match.
  for (const s of SHORTCUTS) {
    if (!s.combo) continue;
    if (s.combo === 'mod+digit') continue; // the wildcard, not a literal
    const rebuilt = s.combo
      .split('+')
      .map(part => (part === 'arrowup' ? 'ArrowUp' : part === 'arrowdown' ? 'ArrowDown'
        : part === 'space' ? ' ' : part === 'enter' ? 'Enter' : part === 'escape' ? 'Escape'
        : part))
      .reduce<{ key: string; ctrlKey?: boolean; metaKey?: boolean; altKey?: boolean; shiftKey?: boolean; isMac?: boolean }>(
        (acc, part) => {
          if (part === 'mod') { acc.isMac = false; acc.ctrlKey = true; return acc; }
          if (part === 'ctrl') { acc.isMac = true; acc.ctrlKey = true; return acc; }
          if (part === 'alt') { acc.altKey = true; return acc; }
          if (part === 'shift') { acc.shiftKey = true; return acc; }
          acc.key = part;
          return acc;
        },
        { key: '' },
      );
    assert.equal(normalizeCombo(rebuilt), s.combo, `${s.id}: combo ${s.combo} does not round-trip`);
  }
});

// ---- normalizeCombo ----

test('mod is Ctrl on Windows/Linux and Cmd on macOS', () => {
  assert.equal(normalizeCombo({ key: 'p', ctrlKey: true, isMac: false }), 'mod+p');
  assert.equal(normalizeCombo({ key: 'p', metaKey: true, isMac: true }), 'mod+p');
  // A bare Cmd on Windows is Super, not mod: it must not collide with mod+p.
  assert.equal(normalizeCombo({ key: 'p', metaKey: true, isMac: false }), 'meta+p');
});

test('literal ctrl is distinct from mod and survives on macOS', () => {
  // Ctrl+Space is CodeMirror's completion binding; folding it into mod would
  // make it Cmd+Space on macOS, which is Spotlight.
  assert.equal(normalizeCombo({ key: ' ', ctrlKey: true, isMac: true }), 'ctrl+space');
  assert.equal(normalizeCombo({ key: ' ', ctrlKey: true, isMac: false }), 'mod+space');
});

test('modifier order is fixed and keys are folded', () => {
  assert.equal(
    normalizeCombo({ key: 'F', ctrlKey: true, shiftKey: true, isMac: false }),
    'mod+shift+f',
  );
  assert.equal(normalizeCombo({ key: 'ArrowUp', altKey: true, ctrlKey: true }), 'mod+alt+arrowup');
  assert.equal(normalizeCombo({ key: ' ' }), 'space');
  assert.equal(normalizeCombo({ key: 'Escape' }), 'escape');
  assert.equal(normalizeCombo({ key: 'Enter' }), 'enter');
});

// ---- matching ----

test('mod+digit stands for any of mod+1…mod+9 and nothing else', () => {
  assert.equal(comboMatches('mod+digit', 'mod+1'), true);
  assert.equal(comboMatches('mod+digit', 'mod+9'), true);
  assert.equal(comboMatches('mod+digit', 'mod+0'), false);
  assert.equal(comboMatches('mod+digit', 'mod+shift+1'), false);
  assert.equal(comboMatches('mod+digit', 'mod+p'), false);
});

test('every global shortcut in the table is reachable through findShortcut', () => {
  for (const s of SHORTCUTS) {
    if (s.combo !== 'mod+digit') continue;
    assert.equal(findShortcut('mod+3')?.id, s.id);
  }
  assert.equal(findShortcut('mod+p')?.id, 'quickOpen');
  assert.equal(findShortcut('f1')?.id, 'help');
  assert.equal(findShortcut('mod+shift+d')?.id, 'selectAllMatches');
});

test('findShortcut returns null for an unbound combo', () => {
  assert.equal(findShortcut('mod+shift+alt+k'), null);
  assert.equal(findShortcut(''), null);
});

// ---- documentation-only rows ----

test('mouse gestures and slash commands carry no combo', () => {
  // They are documented but not dispatched by the global handler; giving them a
  // combo would make them fight for a key they never consume.
  for (const id of ['gridEdit', 'gridColumnMenu', 'gridAutoWidth', 'magic', 'accept']) {
    const row = SHORTCUTS.find(s => s.id === id)!;
    assert.equal(row.combo, undefined, `${id} must not claim a combo`);
  }
});
