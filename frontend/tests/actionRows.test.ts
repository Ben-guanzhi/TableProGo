// The action rows keep their shape.
//
// A row of actions collapses when the window is narrow: the buttons shrink
// below their content, a label breaks into a column of single characters, the
// hints collapse into a block. db-dev hit exactly that — its toolbar_test.js
// records it as "中文标签竖排 (执/行、复/制 M/D)" — and locked the three rules
// that prevent it:
//
//   wrap   — the row may wrap rather than compress its children;
//   nowrap — a button's label does not break between characters;
//   fixed  — a button does not shrink below its content width.
//
// This checks the rows that are always on screen: the window's own action bar,
// the editor's toolbar, the grid's toolbar. A button with only an icon needs no
// nowrap; a button with text does. A row needs the wrap rule so its buttons
// roll to a second line rather than being squeezed.
//
// It reads the source, not pixels, so it works for Tailwind and plain CSS
// alike — and it names the file and the line, so a failure says where to look.

import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const here = path.dirname(fileURLToPath(import.meta.url));
const SRC = path.join(here, '..', 'src');

// The rows that are always on screen, by the marker in their class list. The
// tab bar is left out: it scrolls on purpose (overflow-x-auto) and wrapping it
// would put the tabs on two lines.
const ROWS = [
  { file: 'App.tsx', marker: 'overflow-x-auto min-w-0', skip: ['min-w-0'] },
  { file: 'components/QueryEditor.tsx', marker: 'bg-panel border-b border-line', skip: [] },
  { file: 'components/DataGridToolbar.tsx', marker: 'bg-panel border-b border-line', skip: [] },
];

const CLASS_RE = /class(?:Name)?\s*=\s*["']([^"']*)["']/;

function readLines(rel: string): string[] {
  return fs.readFileSync(path.join(SRC, rel), 'utf8').split(/\r?\n/);
}

// Whether the opening tag ends here, ignoring ">" inside {...}.
function tagEnds(attrs: string): boolean {
  let depth = 0;
  for (const ch of attrs) {
    if (ch === '{') depth++;
    else if (ch === '}') depth = Math.max(0, depth - 1);
    else if (ch === '>' && depth === 0) return true;
  }
  return false;
}

// The buttons in a line range, with the text they show.
function buttonsOf(lines: string[], from: number, to: number) {
  const out: { line: number; classes: string; text: string }[] = [];
  let i = from;
  while (i < Math.min(to, lines.length)) {
    if (!lines[i].includes('<button')) {
      i++;
      continue;
    }
    let attrs = lines[i];
    let j = i;
    while (!tagEnds(attrs) && j + 1 < Math.min(to, lines.length)) {
      j++;
      attrs += ' ' + lines[j];
    }
    const m = CLASS_RE.exec(attrs);
    if (m) {
      const classes = m[1];
      let text = '';
      for (let k = j + 1; k < Math.min(j + 12, lines.length); k++) {
        if (lines[k].includes('</button>')) break;
        text += ' ' + lines[k].trim();
      }
      if (lines[i].includes('>') && lines[i].includes('</button>')) {
        text = lines[i].split('>')[1].split('</button>')[0] + ' ' + text;
      }
      out.push({ line: i + 1, classes, text });
    }
    i = j + 1;
  }
  return out;
}

// Whether the button shows words rather than only an icon. Only the text nodes
// count: a JSX attribute or a component name is not a label.
function hasText(text: string): boolean {
  const plain = text.replace(/<[^>]*>/g, ' ').replace(/\{[^}]*\}/g, ' ');
  return /[A-Za-z0-9一-鿿]+/.test(plain);
}

test('the action rows that are always on screen keep their shape', () => {
  const problems: string[] = [];

  for (const row of ROWS) {
    const lines = readLines(row.file);
    for (let i = 0; i < lines.length; i++) {
      if (!lines[i].includes(row.marker) || !lines[i].includes('flex')) continue;
      const m = CLASS_RE.exec(lines[i]);
      if (!m) continue;
      const rowClasses = m[1];
      // A row that scrolls on purpose must not wrap.
      if (row.skip.some(s => rowClasses.includes(s))) continue;

      if (!rowClasses.includes('flex-wrap')) {
        problems.push(`${row.file}:${i + 1}  the action row cannot wrap: ${rowClasses.slice(0, 100)}`);
      }

      for (const b of buttonsOf(lines, i, i + 40)) {
        const missing: string[] = [];
        if (hasText(b.text) && !b.classes.includes('whitespace-nowrap')) missing.push('nowrap');
        if (!b.classes.includes('shrink-0')) missing.push('fixed');
        if (missing.length) {
          problems.push(`${row.file}:${b.line}  missing: ${missing.join(', ')}\n      ${b.classes.slice(0, 100)}`);
        }
      }
    }
  }

  assert.deepEqual(problems, [], problems.join('\n'));
});
