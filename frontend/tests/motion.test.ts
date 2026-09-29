import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';

import { scrollBehavior } from '../src/motion.ts';

const setMedia = (matches: boolean) => {
  (globalThis as any).matchMedia = () => ({ matches });
};

afterEach(() => {
  delete (globalThis as any).matchMedia;
});

test('scrolls smoothly unless the OS asks for reduced motion', () => {
  setMedia(false);
  assert.equal(scrollBehavior(), 'smooth');

  setMedia(true);
  assert.equal(scrollBehavior(), 'auto');
});

test('falls back to smooth when matchMedia is missing', () => {
  assert.equal(scrollBehavior(), 'smooth');
});
