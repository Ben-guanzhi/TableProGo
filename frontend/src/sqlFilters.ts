import type { SqlFilter, SqlFilterOp } from './types';

// The data card's filter bar: what a condition can say, and when it says
// enough to be worth sending. The SQL itself is built by the backend, so this
// module only keeps the bar honest about the shapes it offers.

export const sqlFilterLabels: Record<SqlFilterOp, string> = {
  eq: '等于',
  ne: '不等于',
  gt: '大于',
  gte: '大于等于',
  lt: '小于',
  lte: '小于等于',
  contains: '包含',
  null: '为空',
  notNull: '不为空',
};

// The order the menu lists them in: the comparisons a user reaches for first.
export const sqlFilterOps: SqlFilterOp[] = [
  'eq',
  'ne',
  'contains',
  'gt',
  'gte',
  'lt',
  'lte',
  'null',
  'notNull',
];

export const filterNeedsValue = (op: SqlFilterOp) => op !== 'null' && op !== 'notNull';

// Mirrors isNumericColumn in filters.go: "包含" compares text, and an engine
// like PostgreSQL will not run it against a number, so the bar does not offer
// it there.
//
// This Set is a language-level mirror of tableprosql.IsNumericColumnType
// (tableprosql/sqlgen.go, via the Go helpers the backend imports) — the Go set
// is the source of truth. It cannot import Go; keep the two name lists in lock
// step when a type is added or renamed there.
const numericTypes = new Set([
  'int', 'integer', 'int2', 'int4', 'int8', 'tinyint', 'smallint', 'mediumint',
  'bigint', 'serial', 'bigserial', 'smallserial', 'decimal', 'dec', 'numeric',
  'fixed', 'float', 'float4', 'float8', 'double', 'double precision', 'real',
  'money', 'smallmoney', 'number', 'binary_float', 'binary_double', 'year',
]);

export const isNumericType = (type: string) => {
  const base = (type || '').toLowerCase().split('(')[0].replace(/\s+unsigned$/, '').trim();
  return numericTypes.has(base);
};

export const opsForType = (type: string): SqlFilterOp[] =>
  isNumericType(type) ? sqlFilterOps.filter(op => op !== 'contains') : sqlFilterOps;

// A condition is worth sending once the column is named and the comparison has
// something to compare with. "等于" with an empty value is left alone on
// purpose: that is how one searches for an empty string, or for NULL in a
// numeric column.
export const filterIsComplete = (filter: SqlFilter) =>
  !!filter.column && (filter.op === 'null' || filter.op === 'notNull' || filter.op === 'eq' || filter.value.trim() !== '');

export const addFilter = (filters: SqlFilter[], filter: SqlFilter): SqlFilter[] => [
  ...filters,
  filter,
];

export const dropFilter = (filters: SqlFilter[], index: number): SqlFilter[] =>
  filters.filter((_, i) => i !== index);

export const describeFilter = (filter: SqlFilter) =>
  `${filter.column} ${sqlFilterLabels[filter.op]}${filterNeedsValue(filter.op) ? ` ${filter.value}` : ''}`;
