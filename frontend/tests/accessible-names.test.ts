import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

// Icon-only controls are invisible to a screen reader, and a drag handle with no
// tabIndex is invisible to the keyboard. Both mistakes are cheap to make again,
// so the source itself is checked - there is no DOM test runner in this project.

const srcDir = fileURLToPath(new URL('../src', import.meta.url));

function tsxFiles(dir: string): string[] {
  return readdirSync(dir).flatMap(entry => {
    const full = `${dir}/${entry}`;
    if (statSync(full).isDirectory()) return tsxFiles(full);
    return entry.endsWith('.tsx') ? [full] : [];
  });
}

// Reads forward from `<tag` to the end of the opening tag, ignoring `>` and
// braces that live inside attribute values.
function openingTag(code: string, from: number): string {
  let i = from;
  let brace = 0;
  let quote = '';
  while (i < code.length) {
    const ch = code[i];
    if (quote) {
      if (ch === quote) quote = '';
    } else if (ch === '"' || ch === "'") {
      quote = ch;
    } else if (ch === '{') {
      brace += 1;
    } else if (ch === '}') {
      brace -= 1;
    } else if (ch === '>' && brace === 0) {
      return code.slice(from, i + 1);
    }
    i += 1;
  }
  return code.slice(from);
}

// A control wrapped in <label> is named by the surrounding text, which is how the
// settings popover labels its checkbox and number field.
function insideLabel(code: string, index: number): boolean {
  const open = code.lastIndexOf('<label', index);
  if (open < 0) return false;
  return !code.slice(open, index).includes('</label>');
}

// Keeps string literals out of {…} expressions (a label rendered as
// {copied ? '已复制' : '复制'} is visible text) and treats an expression with no JSX
// in it ({tab.title}) as a dynamic label.
function visibleText(code: string): string {
  let out = '';
  let depth = 0;
  let expr = '';
  for (let i = 0; i < code.length; i += 1) {
    const ch = code[i];
    if (ch === '{') {
      depth += 1;
      if (depth === 1) expr = '';
      continue;
    }
    if (ch === '}') {
      depth -= 1;
      if (depth === 0 && !expr.includes('<')) out += ` ${expr} `;
      continue;
    }
    if (depth === 0) {
      if (ch === "'" || ch === '"') {
        const end = code.indexOf(ch, i + 1);
        if (end > i) {
          out += code.slice(i + 1, end);
          i = end;
          continue;
        }
      }
      out += ch;
    } else {
      expr += ch;
      if (ch === "'" || ch === '"') {
        const end = code.indexOf(ch, i + 1);
        if (end > i) {
          out += code.slice(i + 1, end);
          i = end;
        }
      }
    }
  }
  return out.replace(/<[^>]*>/g, ' ');
}

const LETTER = /[A-Za-z\u4e00-\u9fff]/;
const unnamedButtons: string[] = [];
const unnamedFields: string[] = [];
const badSeparators: string[] = [];
let separatorCount = 0;

for (const file of tsxFiles(srcDir)) {
  const code = readFileSync(file, 'utf8');
  const where = (index: number) => `${file.replace(srcDir, 'src')}:${code.slice(0, index).split('\n').length}`;

  for (const match of code.matchAll(/<button\b/g)) {
    const tag = openingTag(code, match.index);
    const body = code.slice(match.index + tag.length, code.indexOf('</button>', match.index));
    if (!/aria-label|aria-labelledby/.test(tag) && !LETTER.test(visibleText(body))) {
      unnamedButtons.push(`${where(match.index)} ${tag.replace(/\s+/g, ' ').slice(0, 80)}`);
    }
  }

  // An ARIA separator is a widget, not decoration: it has to be reachable, name
  // what it resizes, and say which way it moves.
  for (const match of code.matchAll(/role=["']separator["']/g)) {
    separatorCount += 1;
    const tag = openingTag(code, code.lastIndexOf('<', match.index));
    const missing = ['aria-label', 'aria-orientation', 'tabIndex'].filter(attr => !tag.includes(attr));
    if (missing.length) badSeparators.push(`${where(match.index)} missing ${missing.join(', ')}`);
  }

  // A placeholder is not a label: it disappears once there is a value, and it is
  // never announced as the field's name while typing.
  for (const match of code.matchAll(/<(input|select|textarea)\b/g)) {
    const tag = openingTag(code, match.index);
    if (/type=["'](hidden|submit)["']/.test(tag)) continue;
    const named = /aria-label|aria-labelledby|\bid=/.test(tag) || insideLabel(code, match.index);
    if (!named) {
      unnamedFields.push(`${where(match.index)} <${match[1]} ${tag.replace(/\s+/g, ' ').slice(1, 70)}`);
    }
  }
}

test('every icon-only button carries an accessible name', () => {
  assert.deepEqual(unnamedButtons, []);
});

test('every resize separator is focusable, labelled and oriented', () => {
  assert.deepEqual(badSeparators, []);
});

test('every form field has a name beyond its placeholder', () => {
  assert.deepEqual(unnamedFields, []);
});

test('the scans actually found markup', () => {
  // Guards the two checks above against a parser that silently matched nothing.
  assert.ok(separatorCount >= 2, `only ${separatorCount} separators parsed`);
});
