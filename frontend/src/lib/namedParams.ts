// Named query parameters: `:name` tokens a user writes instead of literals.
// Scanning and substitution are pure functions here so the rules are testable
// in isolation (see tests/namedParams.test.ts); the editor decides when to ask.

// findNamedParams lists the unique `:name` placeholders in statement order.
// Skipped on purpose: string literals, quoted identifiers, comments, `::`
// casts (PostgreSQL) and positional `:1` markers (Oracle) — none of those is
// a name the user wants to fill in.
export function findNamedParams(sql: string): string[] {
  const seen = new Set<string>();
  const order: string[] = [];
  let i = 0;
  const isNameStart = (c: string) => /[A-Za-z_\u0080-\uffff]/.test(c);
  const isNameByte = (c: string) => /[A-Za-z0-9_\u0080-\uffff]/.test(c);

  while (i < sql.length) {
    const c = sql[i];
    const next = sql[i + 1];
    if (c === '-' && next === '-') {
      while (i < sql.length && sql[i] !== '\n') i++;
      continue;
    }
    if (c === '/' && next === '*') {
      i = sql.indexOf('*/', i + 2);
      i = i < 0 ? sql.length : i + 2;
      continue;
    }
    if (c === "'" || c === '"' || c === '`') {
      i++;
      while (i < sql.length) {
        if (c === "'" && sql[i] === '\\') {
          i += 2;
          continue;
        }
        if (sql[i] === c) {
          if (sql[i + 1] === c) {
            i += 2; // doubled quote stays inside
            continue;
          }
          i++;
          break;
        }
        i++;
      }
      continue;
    }
    if (c === ':' && next === ':') {
      i += 2; // PostgreSQL cast
      continue;
    }
    if (c === ':' && isNameStart(next ?? '')) {
      let j = i + 1;
      while (j < sql.length && isNameByte(sql[j])) j++;
      const name = sql.slice(i + 1, j);
      if (!seen.has(name)) {
        seen.add(name);
        order.push(name);
      }
      i = j;
      continue;
    }
    i++;
  }
  return order;
}

// substituteParams replaces every `:name` with the given value. A value that
// looks like a number stays unquoted; anything else becomes a single-quoted
// literal with the quotes doubled, the one spelling every SQL dialect shares.
// An unknown name (no value given) is left untouched, so the dialog can be
// answered partially and the remaining markers stay visible.
export function substituteParams(sql: string, values: Record<string, string>): string {
  const numeric = (v: string) => /^[+-]?(\d+(\.\d+)?|\.\d+)$/.test(v.trim());
  const literal = (v: string) =>
    numeric(v) ? v.trim() : `'${v.split("'").join("''")}'`;

  // Same scanner as findNamedParams, but rewriting as it goes.
  let out = '';
  let i = 0;
  const isNameStart = (c: string) => /[A-Za-z_\u0080-\uffff]/.test(c);
  const isNameByte = (c: string) => /[A-Za-z0-9_\u0080-\uffff]/.test(c);

  while (i < sql.length) {
    const c = sql[i];
    const next = sql[i + 1];
    if (c === '-' && next === '-') {
      const end = sql.indexOf('\n', i);
      const stop = end < 0 ? sql.length : end;
      out += sql.slice(i, stop);
      i = stop;
      continue;
    }
    if (c === '/' && next === '*') {
      const end = sql.indexOf('*/', i + 2);
      const stop = end < 0 ? sql.length : end + 2;
      out += sql.slice(i, stop);
      i = stop;
      continue;
    }
    if (c === "'" || c === '"' || c === '`') {
      let j = i + 1;
      while (j < sql.length) {
        if (c === "'" && sql[j] === '\\') {
          j += 2;
          continue;
        }
        if (sql[j] === c) {
          if (sql[j + 1] === c) {
            j += 2;
            continue;
          }
          j++;
          break;
        }
        j++;
      }
      out += sql.slice(i, j);
      i = j;
      continue;
    }
    if (c === ':' && next === ':') {
      out += '::';
      i += 2;
      continue;
    }
    if (c === ':' && isNameStart(next ?? '')) {
      let j = i + 1;
      while (j < sql.length && isNameByte(sql[j])) j++;
      const name = sql.slice(i + 1, j);
      const value = values[name];
      if (value !== undefined && value !== '') {
        out += literal(value);
      } else {
        out += sql.slice(i, j);
      }
      i = j;
      continue;
    }
    out += c;
    i++;
  }
  return out;
}
