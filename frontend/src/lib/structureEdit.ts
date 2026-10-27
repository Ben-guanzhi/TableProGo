import type { ForeignKeyInfo, IndexInfo } from '../types/index';

// The structure editor's index/foreign-key tabs: pure row preparation and
// parsing so the dialog component only renders form fields (P2-7). The backend
// diffs the desired lists against the catalog and renders the DDL through the
// kernel ddl package; these helpers only decide what the user gets to edit.

// One row of the index tab. columnsText is the editable comma list; the parse
// back to string[] happens when the payload is built.
export interface IndexEditRow {
  name: string;
  unique: boolean;
  columnsText: string;
  // isNew marks rows the user added this session (highlighted in the grid).
  isNew?: boolean;
  // locked rows render read-only: the form only edits plain columns, so an
  // index whose key parts are expressions (or that carries a WHERE predicate)
  // would come out degraded if the user could touch it — recreating it would
  // silently drop the expression or predicate.
  locked: boolean;
  reason?: string;
}

// One row of the foreign-key tab.
export interface ForeignKeyEditRow {
  name: string;
  columnsText: string;
  refSchema: string;
  refTable: string;
  refColumnsText: string;
  onDelete: string;
  onUpdate: string;
  isNew?: boolean;
}

// The standard spellings the backend compares (dbcore.NormalizeFKAction);
// the action select offers exactly these.
export const FK_ACTIONS = ['NO ACTION', 'RESTRICT', 'CASCADE', 'SET NULL', 'SET DEFAULT'] as const;

// normalizeFKAction maps catalog spellings onto the standard ones so an
// untouched key round-trips as a no-op and the select shows the right option
// (MSSQL reports NO_ACTION; the catalog may use lowercase).
export function normalizeFKAction(action: string): string {
  const upper = (action ?? '').trim().toUpperCase().replace(/_/g, ' ');
  if (upper === '') return '';
  return FK_ACTIONS.find(a => a === upper) ?? upper;
}

// parseColumnList reads the comma list of a columns cell; empty fragments are
// dropped so "a,,b" and trailing commas behave like the user expects.
export function parseColumnList(text: string): string[] {
  return text
    .split(',')
    .map(c => c.trim())
    .filter(c => c.length > 0);
}

export const formatColumnList = (cols: string[]): string => cols.join(', ');

// indexEditRows prepares the secondary indexes: the primary key and SQLite's
// constraint autoindexes are not the editor's to drop, and expression/predicate
// indexes are locked rather than shown editable.
export function indexEditRows(indexes: IndexInfo[]): IndexEditRow[] {
  return indexes
    .filter(i => !i.primary && !i.name.startsWith('sqlite_autoindex_'))
    .map(i => {
      const locked = i.columns.length === 0 || !!i.predicate;
      return {
        name: i.name,
        unique: i.unique,
        columnsText: formatColumnList(i.columns),
        locked,
        reason: locked ? (i.predicate ? '部分索引（WHERE …）不在本表单编辑' : '含表达式的索引不在本表单编辑') : undefined,
      };
    });
}

// foreignKeyEditRows prepares the FK list for the editor.
export function foreignKeyEditRows(fks: ForeignKeyInfo[]): ForeignKeyEditRow[] {
  return fks.map(fk => ({
    name: fk.name,
    columnsText: formatColumnList(fk.columns),
    refSchema: fk.refSchema ?? '',
    refTable: fk.refTable,
    refColumnsText: formatColumnList(fk.refColumns),
    onDelete: normalizeFKAction(fk.onDelete),
    onUpdate: normalizeFKAction(fk.onUpdate),
  }));
}
