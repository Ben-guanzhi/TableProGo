import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

// i18n gate for the translated dialog components (POLISH-PLAN P1-4): the source
// is scanned for visible UI text, and any ASCII fragment that is not a known
// technical token is a failure. There is no DOM test runner in this project, so
// the check reads the source directly.
//
// Scope is deliberately the dialogs named in the plan; adding a component here
// is the act of declaring it translated, and the gate then locks it.

const DIALOGS = [
  'components/ConnectionDialog.tsx',
  'components/DatabaseFilterDialog.tsx',
];

// Technical vocabulary that must stay verbatim: product names, SQL/DDL and
// Redis keywords the user recognises, ARIA roles, keyboard keys, booleans and
// example values. Kept as exact normalized fragments.
const ALLOW = new Set([
  // ARIA roles and states
  'dialog', 'button', 'listbox', 'option', 'treeitem', 'separator',
  'presentation', 'alert', 'status', 'group', 'tab', 'tabpanel', 'toolbar',
  'scrollbar', 'slider', 'progressbar', 'tooltip', 'searchbox', 'textbox',
  'checkbox', 'radio', 'menu', 'menuitem', 'combobox',
  // booleans / nullish
  'true', 'false', 'null', 'undefined',
  // keyboard keys
  'Enter', 'Escape', 'Esc', 'Tab', 'ArrowUp', 'ArrowDown', 'ArrowLeft',
  'ArrowRight', 'Space', 'Backspace', 'Delete', 'Home', 'End', 'PageUp',
  'PageDown', 'Control', 'Shift', 'Alt', 'Meta',
  // products and protocols
  'MySQL', 'PostgreSQL', 'SQLite', 'SQL Server', 'Oracle', 'Redis', 'SSH',
  'TLS', 'SSL', 'SQL', 'CSV', 'JSON', 'Markdown', 'Excel（XLSX）', 'XLSX',
  'XML', 'HTML', 'API', 'URL', 'URI', 'HTTP', 'HTTPS', 'ID', 'UUID',
  // engine / type identifiers and input types
  'mysql', 'postgresql', 'sqlite', 'sqlserver', 'oracle', 'redis',
  'text', 'number', 'password', 'checkbox', 'success', 'failed',
  // SQL / DDL keywords shown to the user
  'PRIMARY KEY', 'FOREIGN KEY', 'UNIQUE', 'NOT NULL', 'NULL', 'DEFAULT',
  'CHECK', 'CONSTRAINT', 'INDEX', 'TABLE', 'VIEW', 'COLUMN', 'DROP', 'ALTER',
  'ADD', 'RENAME', 'TRUNCATE', 'CASCADE', 'RESTRICT', 'NO ACTION', 'SET',
  'ASC', 'DESC', 'ON DELETE', 'ON UPDATE', 'AUTO_INCREMENT', 'SERIAL',
  'BIGINT', 'VARCHAR', 'TEXT', 'INT', 'BOOLEAN', 'TIMESTAMP', 'DATE', 'TIME',
  'DECIMAL', 'NUMERIC', 'FLOAT', 'DOUBLE', 'JSONB', 'BLOB', 'BYTEA',
  // SQL standard isolation levels (connection dialog's select options)
  'READ UNCOMMITTED', 'READ COMMITTED', 'REPEATABLE READ', 'SERIALIZABLE',
  // Redis commands and key types shown in the UI
  'TTL', 'SADD', 'SREM', 'SMEMBERS', 'XADD', 'XRANGE', 'EXPIREAT', 'TYPE',
  'KEYS', 'SCAN', 'HSET', 'HGET', 'HGETALL', 'LPUSH', 'RPUSH', 'LRANGE',
  'ZADD', 'ZRANGE', 'ZSCORE', 'INCR', 'DECR', 'APPEND', 'STRLEN', 'SUBSTR',
  'PEXPIRE', 'PERSIST', 'EXISTS',
  // SSL / TLS modes and options
  'disable', 'strict', 'require', 'verify-full', 'verify-ca',
  // example / placeholder values and paths
  'localhost', 'ORCL', 'deploy', 'root', 'system', 'bastion.example.com',
  'C:\\Users\\me\\.ssh\\id_ed25519', '/path/to/database.db',
  // patterns and sample data
  'APP*', '*_Env', 'tempdb', '*', '%', '_', 'data orders.csv',
  'orders, users',
]);

const CJK = /[\u4e00-\u9fff]/;
const LETTER = /[A-Za-z]/;

// Reads forward from `<tag` to the end of the opening tag, ignoring `>` and
// braces that live inside attribute values.
function openingTag(code: string, from: number, end: number): string {
  let i = from;
  let brace = 0;
  let quote = '';
  while (i < end) {
    const ch = code[i];
    if (quote) {
      if (ch === quote) quote = '';
    } else if (ch === '"' || ch === "'" || ch === '`') {
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
  return code.slice(from, end);
}

// Values of the attributes that carry user-visible text, including quoted
// strings inside an expression value (aria-label={isEdit ? 'A' : 'B'}).
function attrTexts(tag: string): string[] {
  const out: string[] = [];
  const re = /(placeholder|title|aria-label|label|content)\s*=\s*(.*)$/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(tag)) !== null) {
    const raw = m[2];
    const inner = /^[`'"](.*)[`'"]$/s.exec(raw)?.[1];
    if (inner !== undefined) {
      out.push(inner);
    } else if (raw.startsWith('{')) {
      out.push(...exprStringLiterals(raw));
    }
  }
  return out;
}

// Quoted string literals inside a `{…}` expression, skipping nested JSX and
// comments so a class-name template literal is never read as UI text.
function exprStringLiterals(code: string): string[] {
  const out: string[] = [];
  let i = 0;
  while (i < code.length) {
    const ch = code[i];
    if (ch === '/' && code[i + 1] === '*') {
      const close = code.indexOf('*/', i + 2);
      i = close < 0 ? code.length : close + 2;
      continue;
    }
    if (ch === '/' && code[i + 1] === '/') {
      const nl = code.indexOf('\n', i);
      i = nl < 0 ? code.length : nl;
      continue;
    }
    if (ch === '<' && /[A-Za-z]/.test(code[i + 1] ?? '')) {
      const tag = openingTag(code, i, code.length);
      i += tag.length;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === '`') {
      const q = ch;
      let j = i + 1;
      while (j < code.length && code[j] !== q) {
        if (code[j] === '\\') j += 1;
        j += 1;
      }
      out.push(code.slice(i + 1, j));
      i = j + 1;
      continue;
    }
    i += 1;
  }
  return out;
}

// Collects text from a `{…}` expression: string literals, plus any JSX nested
// inside it (a conditional renders real children with their own labels).
function exprTexts(code: string, open: number, end: number): { texts: string[]; next: number } {
  const texts: string[] = [];
  let depth = 1;
  let i = open + 1;
  while (i < end && depth > 0) {
    const ch = code[i];
    if (ch === '/' && code[i + 1] === '*') {
      const close = code.indexOf('*/', i + 2);
      i = close < 0 ? end : close + 2;
      continue;
    }
    if (ch === '/' && code[i + 1] === '/') {
      const nl = code.indexOf('\n', i);
      i = nl < 0 ? end : nl;
      continue;
    }
    if (ch === '{') {
      depth += 1;
    } else if (ch === '}') {
      depth -= 1;
      if (depth === 0) break;
    } else if (ch === '"' || ch === "'" || ch === '`') {
      const q = ch;
      let j = i + 1;
      while (j < end && code[j] !== q) {
        if (code[j] === '\\') j += 1;
        j += 1;
      }
      texts.push(code.slice(i + 1, j));
      i = j + 1;
      continue;
    } else if (ch === '<' && /[A-Za-z]/.test(code[i + 1] ?? '')) {
      const tag = openingTag(code, i, end);
      if (tag.startsWith('</')) {
        i += tag.length;
        continue;
      }
      for (const v of attrTexts(tag)) texts.push(v);
      if (tag.endsWith('/>')) {
        i += tag.length;
        continue;
      }
      // Recurse into the element's children, stopping at its close tag.
      i = elementTexts(code, i + tag.length, end, texts);
      continue;
    }
    i += 1;
  }
  return { texts, next: i + 1 };
}

// Parses the children of an opened element, collecting literal text nodes and
// i18n attributes until the matching closing tag.
function elementTexts(code: string, i: number, end: number, out: string[]): number {
  let depth = 1;
  while (i < end && depth > 0) {
    const ch = code[i];
    if (ch === '/' && code[i + 1] === '*') {
      const close = code.indexOf('*/', i + 2);
      i = close < 0 ? end : close + 2;
      continue;
    }
    if (ch === '/' && code[i + 1] === '/') {
      const nl = code.indexOf('\n', i);
      i = nl < 0 ? end : nl;
      continue;
    }
    if (ch === '<' && code[i + 1] === '/') {
      const tag = openingTag(code, i, end);
      i += tag.length;
      depth -= 1;
      continue;
    }
    if (ch === '<' && /[A-Za-z]/.test(code[i + 1] ?? '')) {
      const tag = openingTag(code, i, end);
      for (const v of attrTexts(tag)) out.push(v);
      if (tag.endsWith('/>')) {
        i += tag.length;
        continue;
      }
      i = elementTexts(code, i + tag.length, end, out);
      continue;
    }
    if (ch === '{') {
      const { texts, next } = exprTexts(code, i, end);
      out.push(...texts);
      i = next;
      continue;
    }
    if (depth === 1) {
      // Literal text of the outermost element only; deeper text is reached by
      // recursion. Whitespace-only runs are not user-visible.
      let j = i;
      while (j < end && code[j] !== '<' && code[j] !== '{') j += 1;
      const norm = code.slice(i, j).replace(/\s+/g, ' ').trim();
      if (norm) out.push(norm);
      i = j;
      continue;
    }
    i += 1;
  }
  return i;
}

// The body of each `return ( … )`, found by matching balanced parentheses and
// braces. Anything outside these regions is code, not JSX.
function returnRegions(code: string): Array<[number, number]> {
  const regions: Array<[number, number]> = [];
  const re = /\breturn\s*\(/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(code)) !== null) {
    const open = code.indexOf('(', m.index);
    let depth = 0;
    let i = open;
    let quote = '';
    while (i < code.length) {
      const ch = code[i];
      if (quote) {
        if (ch === quote) quote = '';
      } else if (ch === '"' || ch === "'" || ch === '`') {
        quote = ch;
      } else if (ch === '/' && code[i + 1] === '/') {
        const nl = code.indexOf('\n', i);
        i = nl < 0 ? code.length : nl;
        continue;
      } else if (ch === '/' && code[i + 1] === '*') {
        const close = code.indexOf('*/', i + 2);
        i = close < 0 ? code.length : close + 2;
        continue;
      } else if (ch === '(' || ch === '{') {
        depth += 1;
      } else if (ch === ')' || ch === '}') {
        depth -= 1;
        if (depth === 0) {
          i += 1;
          break;
        }
      }
      i += 1;
    }
    regions.push([open + 1, i]);
  }
  return regions;
}

function extractTexts(code: string): string[] {
  const out: string[] = [];
  for (const [start, end] of returnRegions(code)) {
    elementTexts(code, start, end, out);
  }
  return out;
}

const violations: string[] = [];
const scanned: Array<{ rel: string; count: number; cjk: number }> = [];
for (const rel of DIALOGS) {
  const file = fileURLToPath(new URL(`../src/${rel}`, import.meta.url));
  const code = readFileSync(file, 'utf8');
  let count = 0;
  let cjk = 0;
  for (const frag of extractTexts(code)) {
    const norm = frag.replace(/\s+/g, ' ').trim();
    if (!norm) continue;
    count += 1;
    if (CJK.test(norm)) cjk += 1;
    if (!LETTER.test(norm)) continue;
    if (CJK.test(norm)) continue;
    if (ALLOW.has(norm)) continue;
    violations.push(`${rel}: ${JSON.stringify(norm)}`);
  }
  scanned.push({ rel, count, cjk });
}

test('translated dialogs render no non-allowlisted English UI text', () => {
  assert.deepEqual(violations, []);
});

test('the i18n scan actually read the dialogs', () => {
  // Guards the check above against a parser that silently matched nothing.
  assert.ok(
    scanned.every(s => s.count > 10 && s.cjk > 5),
    `scan counts too small: ${JSON.stringify(scanned)}`,
  );
});
