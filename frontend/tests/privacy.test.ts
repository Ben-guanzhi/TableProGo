import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  MASK,
  cellMask,
  detailLine,
  fileStem,
  hostPort,
  identityLabel,
  maskText,
  privateTitle,
  shouldBlur,
} from '../src/lib/privacy.ts';

test('maskText hides the value and its length', () => {
  // A screen capture must not reveal how long a host or database name is: `db1`
  // and `db-production-2024` mask to the same width.
  assert.equal(maskText('db1'), MASK);
  assert.equal(maskText('db-production-2024'), MASK);
  assert.equal(maskText('db1').length, maskText('db-production-2024').length);
  // Nothing to hide stays nothing: an empty field must not grow a mask.
  assert.equal(maskText(''), '');
  assert.equal(maskText(null), '');
  assert.equal(maskText(undefined), '');
});

test('hostPort masks host and port together', () => {
  // Composing before masking matters: masking each side separately would leave
  // the real port, and the port alone often names the environment.
  assert.equal(hostPort('db.internal', 3306, true), MASK);
  assert.equal(hostPort('db.internal', 3306, false), 'db.internal:3306');
  // Port-only and host-only still compose sensibly when not private.
  assert.equal(hostPort(undefined, 5432, false), '5432');
  assert.equal(hostPort('h', undefined, false), 'h');
  assert.equal(hostPort('', undefined, false), '');
  // Private + empty is empty, not a lone mask.
  assert.equal(hostPort('', undefined, true), '');
});

test('identityLabel keeps the engine and hides identity', () => {
  const target = { name: 'prod-db.example.com', engine: 'postgresql', host: 'prod-db.example.com', port: 5432, database: 'shop' };
  assert.equal(identityLabel(target, false), 'prod-db.example.com');
  const masked = identityLabel(target, true);
  // The engine is not sensitive and helps the user keep their bearings.
  assert.ok(masked.includes('postgresql'), `masked = ${masked}`);
  // The host and the database must be gone.
  assert.ok(!masked.includes('prod-db'), `masked = ${masked}`);
  assert.ok(!masked.includes('shop'), `masked = ${masked}`);
});

test('identityLabel falls back to host/db when there is no name', () => {
  assert.equal(
    identityLabel({ host: 'h', port: 1, database: 'd' }, false),
    'h:1 · d',
  );
  // Private: the composed form collapses to one mask plus the engine.
  assert.equal(identityLabel({ host: 'h', database: 'd', engine: 'mysql' }, true), `${MASK} (mysql)`);
  // And with no engine, just the mask.
  assert.equal(identityLabel({ host: 'h' }, true), MASK);
});

test('detailLine collapses every identity part into one mask', () => {
  const t = { user: 'alice', host: 'h', port: 5432, database: 'shop' };
  assert.equal(detailLine(t, false), 'alice · h:5432 · shop');
  // One mask, not three: counting the masks would say how many fields exist.
  assert.equal(detailLine(t, true), MASK);
  assert.equal(detailLine({}, true), '');
});

test('fileStem does not leak the database into exported filenames', () => {
  // Masking the UI is not enough: an export called shop-prod-orders.xlsx leaves
  // the database name on disk and in every chat it is pasted into.
  assert.equal(fileStem(['shop-prod', 'orders'], false), 'shop-prod-orders');
  assert.equal(fileStem(['shop-prod', 'orders'], true), 'export');
  // Path-hostile characters are replaced rather than producing an invalid file.
  assert.equal(fileStem(['a/b:c', 'd*e'], false), 'a_b_c-d_e');
  assert.equal(fileStem([], false), 'export');
});

test('cellMask is fixed width', () => {
  assert.equal(cellMask(), MASK);
  assert.equal(cellMask().length, MASK.length);
});

test('shouldBlur reveals on hover but never while private and idle', () => {
  assert.equal(shouldBlur(true, false), true);
  assert.equal(shouldBlur(true, true), false, 'the user may peek at what they are working on');
  assert.equal(shouldBlur(false, false), false);
});
test('privateTitle drops the tooltip entirely in privacy mode', () => {
  // A native tooltip is drawn by the browser and the .privacy CSS cannot reach it, so
  // masking the text is not enough — the tooltip must not exist at all.
  assert.equal(privateTitle('shop.orders', false), 'shop.orders');
  assert.equal(privateTitle('shop.orders', true), undefined);
  assert.equal(privateTitle(undefined, false), undefined);
  assert.equal(privateTitle('', true), undefined);
});
