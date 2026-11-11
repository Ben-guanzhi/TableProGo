import type { ERFK } from '../types/index';
import { FK_ACTIONS, normalizeFKAction } from './structureEdit.ts';

// Pure helpers behind the ERD foreign-key editor: they decide what a click on
// the diagram means before anything is sent, so the component only renders. The
// backend still owns identifier validation and dialect rendering.

// The three operations the diagram exposes; the backend rejects anything else.
export type FKEditAction = 'add' | 'drop' | 'rollback';

// The payload shape of POST /api/er/fk. `preview` selects render-only, mirroring
// the structure editor.
export interface FKEditPayload {
  connId: string;
  database: string;
  schema: string;
  table: string;
  action: FKEditAction;
  preview: boolean;
  // add (and a rollback of a drop)
  name?: string;
  columns?: string[];
  refSchema?: string;
  refTable?: string;
  refColumns?: string[];
  onDelete?: string;
  onUpdate?: string;
  // drop (and a rollback of an add)
  fkName?: string;
  rollbackOf?: string;
}

export interface FKEditTarget {
  connId: string;
  database: string;
  schema: string;
  table: string;
}

// parseFKEditAction turns a free-form action into one of the three recognized
// operations, refusing anything else with a message the diagram can show.
export function parseFKEditAction(raw: string): FKEditAction {
  const value = (raw ?? '').trim().toLowerCase();
  if (value === 'add' || value === 'drop' || value === 'rollback') return value;
  throw new Error(`未知的外键操作 "${raw}"（应为 add / drop / rollback）`);
}

// trimList drops empty fragments and surrounding whitespace.
export function trimList(list: string[] | undefined): string[] {
  return (list ?? []).map(c => c.trim()).filter(c => c.length > 0);
}

// normalizeRefColumns aligns the reference list to the source column count: a
// short reference is padded with the source column at the same position (a
// common mistake when the picker leaves the tail empty), a long one is trimmed.
// It returns the same list when nothing needs adjusting.
export function normalizeRefColumns(refColumns: string[] | undefined, columns: string[]): string[] {
  const source = trimList(columns);
  if (source.length === 0) return [];
  const ref = trimList(refColumns);
  const out = ref.slice(0, source.length);
  for (let i = out.length; i < source.length; i += 1) {
    out.push(source[i]);
  }
  return out;
}

// fkDefinitionFromER projects a diagram edge onto the request's add-side fields,
// normalising the actions so the backend compares standard spellings.
export function fkDefinitionFromER(fk: ERFK): {
  name: string;
  columns: string[];
  refSchema: string;
  refTable: string;
  refColumns: string[];
  onDelete: string;
  onUpdate: string;
} {
  const columns = trimList(fk.columns);
  return {
    name: fk.name.trim(),
    columns,
    refSchema: (fk.refSchema ?? '').trim(),
    refTable: fk.refTable.trim(),
    refColumns: normalizeRefColumns(fk.refColumns, columns),
    onDelete: normalizeFKAction(fk.onDelete),
    onUpdate: normalizeFKAction(fk.onUpdate),
  };
}

// buildFKAddPayload builds a render/apply request for a new key. A missing name
// or source column list is refused here so the click never sends an empty key.
export function buildFKAddPayload(target: FKEditTarget, fk: ERFK, preview = false): FKEditPayload {
  const definition = fkDefinitionFromER(fk);
  if (!definition.name) throw new Error('外键名为空');
  if (definition.columns.length === 0) throw new Error(`外键 ${definition.name} 没有本表列`);
  if (!definition.refTable) throw new Error(`外键 ${definition.name} 没有引用表`);
  return {
    ...target,
    action: 'add',
    preview,
    ...definition,
  };
}

// buildFKDropPayload builds a render/apply request for dropping one existing key.
export function buildFKDropPayload(target: FKEditTarget, fkName: string, preview = false): FKEditPayload {
  const name = fkName.trim();
  if (!name) throw new Error('删除外键需要提供外键名');
  return { ...target, action: 'drop', preview, fkName: name };
}

// buildFKRollbackPayload builds the request that undoes an applied add or drop.
// The definition is sent when it is known so the undo can itself be undone.
export function buildFKRollbackPayload(
  target: FKEditTarget,
  opts: { fkName: string; rollbackOf: FKEditAction; fk?: ERFK },
  preview = false,
): FKEditPayload {
  const name = opts.fkName.trim();
  if (!name) throw new Error('回滚外键需要提供外键名');
  if (opts.rollbackOf !== 'add' && opts.rollbackOf !== 'drop') {
    throw new Error(`未知的回滚方向 "${opts.rollbackOf}"（应为 add / drop）`);
  }
  const payload: FKEditPayload = { ...target, action: 'rollback', preview, fkName: name, rollbackOf: opts.rollbackOf };
  if (opts.fk) {
    payload.name = opts.fk.name.trim();
    payload.columns = trimList(opts.fk.columns);
    payload.refSchema = (opts.fk.refSchema ?? '').trim();
    payload.refTable = opts.fk.refTable.trim();
    payload.refColumns = normalizeRefColumns(opts.fk.refColumns, trimList(opts.fk.columns));
    payload.onDelete = normalizeFKAction(opts.fk.onDelete);
    payload.onUpdate = normalizeFKAction(opts.fk.onUpdate);
  }
  return payload;
}

// fkLabel renders one edge the way the diagram labels it, for confirmations.
export function fkLabel(fk: ERFK): string {
  const columns = trimList(fk.columns).join(', ');
  return `${fk.name || '(未命名)'}（${columns} → ${fk.refTable || '?'}）`;
}

export { FK_ACTIONS };
