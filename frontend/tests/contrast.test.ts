import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

// The UI paints its text tiers over --canvas (app shell) and --panel
// (sidebar/grid/cards), so those are the pairs WCAG has to hold for.
const BACKGROUNDS = ['canvas', 'panel'] as const;
const TEXT_TIERS = ['ink', 'secondary', 'muted'] as const;
// Small dense labels are the worst case for 11px text, so the tiers all have to
// clear the AA threshold for small text rather than the 3:1 large-text bar.
const MIN_RATIO = 4.5;

// Status colours are used as small text too (grid badges, error lines), so they
// have to clear the same bar as the text tiers.
const STATUS_COLORS = ['success', 'danger', 'warning'] as const;
// …and as solid button fills, where the label is the `-on-*` token.
const FILL_LABELS: [string, string][] = [
  ['on-accent', 'accent'],
  ['on-success', 'success'],
  ['on-danger', 'danger'],
];

// --accent is also the colour of small text and icons (the sidebar's database
// glyph, the grid's sort arrows, the editor's keywords), so it has to clear the
// text bar too. Where it is painted on its own low-alpha tint - the quick-open
// row, the located-column highlight - it is only ever an icon, which is held to
// 3:1 rather than 4.5:1.
const ACCENT_TEXT_MIN = 4.5;

const css = readFileSync(fileURLToPath(new URL('../src/index.css', import.meta.url)), 'utf8');

type Tokens = Record<string, string>;

function tokensOf(theme: 'dark' | 'light'): Tokens {
  // The dark block is `:root, :root[data-theme='dark'] {…}` and comes first,
  // so everything before the light selector belongs to it.
  const lightStart = css.indexOf(":root[data-theme='light']");
  const darkEnd = css.indexOf('}', 0);
  const slice =
    theme === 'dark' ? css.slice(0, darkEnd) : css.slice(lightStart, css.indexOf('}', lightStart));
  const out: Tokens = {};
  for (const match of slice.matchAll(/--([\w-]+):\s*([\d.]+)\s+([\d.]+)\s+([\d.]+);/g)) {
    out[match[1]] = [Number(match[2]), Number(match[3]), Number(match[4])].join(',');
  }
  return out;
}

function channelLuminance(value: number): number {
  const s = value / 255;
  return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
}

function luminance(rgb: string): number {
  const [r, g, b] = rgb.split(',').map(Number);
  return 0.2126 * channelLuminance(r) + 0.7152 * channelLuminance(g) + 0.0722 * channelLuminance(b);
}

function contrastRatio(foreground: string, background: string): number {
  const a = luminance(foreground);
  const b = luminance(background);
  const [high, low] = a > b ? [a, b] : [b, a];
  return (high + 0.05) / (low + 0.05);
}

// `bg-success/10` style row tints: the status colour at low alpha over a surface.
function tint(color: string, over: string, alpha: number): string {
  const c = color.split(',').map(Number);
  const b = over.split(',').map(Number);
  return c.map((v, i) => Math.round(v * alpha + b[i] * (1 - alpha))).join(',');
}

for (const theme of ['dark', 'light'] as const) {
  for (const tier of TEXT_TIERS) {
    for (const background of BACKGROUNDS) {
      test(`${theme}: --${tier} on --${background} meets AA for small text`, () => {
        const tokens = tokensOf(theme);
        const ratio = contrastRatio(tokens[tier], tokens[background]);
        assert.ok(
          ratio >= MIN_RATIO,
          `--${tier} on --${background} is ${ratio.toFixed(2)}:1, needs >=${MIN_RATIO}:1`,
        );
      });
    }
  }

  test(`${theme}: text tiers stay ordered by emphasis`, () => {
    const tokens = tokensOf(theme);
    const onPanel = (tier: string) => contrastRatio(tokens[tier], tokens.panel);
    assert.ok(onPanel('ink') > onPanel('secondary'));
    assert.ok(onPanel('secondary') > onPanel('muted'));
  });

  for (const background of BACKGROUNDS) {
    test(`${theme}: --accent reads as small text on --${background}`, () => {
      const tokens = tokensOf(theme);
      const ratio = contrastRatio(tokens.accent, tokens[background]);
      assert.ok(
        ratio >= ACCENT_TEXT_MIN,
        `--accent on --${background} is ${ratio.toFixed(2)}:1, needs >=${ACCENT_TEXT_MIN}:1`,
      );
    });
  }

  for (const status of STATUS_COLORS) {
    for (const background of BACKGROUNDS) {
      test(`${theme}: --${status} text on --${background} meets AA`, () => {
        const tokens = tokensOf(theme);
        const ratio = contrastRatio(tokens[status], tokens[background]);
        assert.ok(
          ratio >= MIN_RATIO,
          `--${status} on --${background} is ${ratio.toFixed(2)}:1, needs >=${MIN_RATIO}:1`,
        );
      });
    }
  }

  for (const [status, alpha] of [['success', 0.1], ['danger', 0.15]] as [string, number][]) {
    for (const background of BACKGROUNDS) {
      test(`${theme}: --${status} text still reads on its own row tint`, () => {
        const tokens = tokensOf(theme);
        const ratio = contrastRatio(tokens[status], tint(tokens[status], tokens[background], alpha));
        assert.ok(
          ratio >= MIN_RATIO,
          `--${status} on ${alpha * 100}% --${status}/${background} is ${ratio.toFixed(2)}:1`,
        );
      });
    }
  }

  for (const [label, fill] of FILL_LABELS) {
    test(`${theme}: --${label} reads on a --${fill} button`, () => {
      const tokens = tokensOf(theme);
      const ratio = contrastRatio(tokens[label], tokens[fill]);
      assert.ok(
        ratio >= MIN_RATIO,
        `--${label} on --${fill} is ${ratio.toFixed(2)}:1, needs >=${MIN_RATIO}:1`,
      );
    });
  }
}
