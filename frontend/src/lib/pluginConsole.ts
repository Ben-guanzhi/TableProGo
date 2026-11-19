// W17: pure helpers for the plugin console.
//
// Kept out of the component so they can be tested without React. The only interesting
// decisions live here: how a form's strings become the JSON a plugin expects, and how a
// result cell is rendered without turning NULL into "".

import type { PluginActionParam } from '../api';

// initialValues pre-fills a form from the params' declared defaults.
export function initialValues(params: PluginActionParam[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const p of params) {
    const d = p.default ?? '';
    // A select's legacy default is a JSON array of CHOICES, not a value:
    // pre-filling the field with it would show ["a","b"] as the selection.
    if (p.kind === 'select' && (p.options?.length || /^\s*\[/.test(d))) {
      out[p.name] = '';
    } else {
      out[p.name] = d;
    }
  }
  return out;
}

// argsOf turns the form's strings into the JSON a plugin expects.
//
// A number field holding digits goes as a number, so a topK is not the string "10"; a
// field that is not a number stays a string (a vector is a comma-separated string, an
// id may be text). Empty optional fields are omitted rather than sent as "" — a plugin
// distinguishes "not given" (use its default) from "given as empty".
export function argsOf(
  params: PluginActionParam[],
  values: Record<string, string>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const p of params) {
    const raw = (values[p.name] ?? '').trim();
    if (raw === '') continue;
    if (p.kind === 'number' && /^-?\d+(\.\d+)?$/.test(raw)) {
      out[p.name] = Number(raw);
    } else if (p.kind === 'checkbox' && (raw === 'true' || raw === 'false')) {
      out[p.name] = raw === 'true'; // a checkbox is a boolean, not "true"
    } else if (p.multiple && /^\s*\[/.test(raw)) {
      try { out[p.name] = JSON.parse(raw); } catch { out[p.name] = raw; }
    } else {
      out[p.name] = raw;
    }
  }
  return out;
}

// missingRequired names the first required field that is blank, so the console can say
// which one instead of relaying a plugin-side validation error.
export function missingRequired(
  params: PluginActionParam[],
  values: Record<string, string>,
): string {
  for (const p of params) {
    if (p.required && (values[p.name] ?? '').trim() === '') {
      return p.label || p.name;
    }
  }
  return '';
}

// cellText renders one result cell. The server maps kernel values to plain JSON, so this
// is mostly String(); NULL is spelled out because an empty cell would read as "".
export function cellText(v: unknown): string {
  if (v === null || v === undefined) return 'NULL';
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  return JSON.stringify(v);
}