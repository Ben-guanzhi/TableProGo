import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  FORMAT_PRESETS,
  normalizePreset,
  presetHint,
  presetLabel,
} from '../src/lib/formatPresets.ts';

test('the offered presets are exactly the ones the backend understands', () => {
  // A dropdown entry the Go formatter would silently map to "default" is a lie
  // in the UI; this pins the two lists together.
  assert.deepEqual(FORMAT_PRESETS.map(p => p.id), ['default', 'compact', 'expanded']);
});

test('normalizePreset folds case, trims, and falls back to default', () => {
  assert.equal(normalizePreset('compact'), 'compact');
  assert.equal(normalizePreset('  COMPACT '), 'compact');
  assert.equal(normalizePreset('Expanded'), 'expanded');
  for (const unknown of ['', '   ', undefined, null, 'bogus', 'default-typo']) {
    assert.equal(normalizePreset(unknown), 'default', `${String(unknown)} should mean default`);
  }
});

test('labels and hints always resolve, even for an unknown stored value', () => {
  for (const id of FORMAT_PRESETS.map(p => p.id)) {
    assert.ok(presetLabel(id).length > 0);
    assert.ok(presetHint(id).length > 0);
  }
  // A settings file carrying a typo must render as the default, not as raw text.
  assert.equal(presetLabel('nonsense'), presetLabel('default'));
  assert.equal(presetHint('nonsense'), presetHint('default'));
});
