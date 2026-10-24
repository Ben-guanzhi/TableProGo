// A Redis value is rendered as a grid, but only some cells identify a writable
// element. This maps (type, row, double-clicked column) onto the element that
// dbcore.ElementWriter can replace in place: a string's value, a hash field's
// value, a list item's value, or a sorted-set member's score.
export interface KVEditTarget {
  identity: 'key' | 'field' | 'index' | 'member';
  id: string;
  column: 'value' | 'score';
  current: string;
  // What the dialog calls the element it is about to overwrite.
  label: string;
}

const text = (value: unknown) => (value === null || value === undefined ? '' : String(value));

export function kvEditTarget(
  type: string,
  row: unknown[] | undefined,
  column: string,
): KVEditTarget | null {
  if (!row) return null;
  switch (type) {
    case 'string':
      if (column !== 'value') return null;
      return { identity: 'key', id: '', column: 'value', current: text(row[0]), label: '字符串值' };
    case 'hash':
      if (column !== 'value') return null;
      return {
        identity: 'field',
        id: text(row[0]),
        column: 'value',
        current: text(row[1]),
        label: `字段 ${text(row[0])}`,
      };
    case 'list':
      if (column !== 'value') return null;
      return {
        identity: 'index',
        id: text(row[0]),
        column: 'value',
        current: text(row[1]),
        label: `第 ${Number(row[0]) + 1} 项`,
      };
    case 'zset':
      if (column !== 'score') return null;
      return {
        identity: 'member',
        id: text(row[0]),
        column: 'score',
        current: text(row[1]),
        label: `成员 ${text(row[0])} 的分数`,
      };
    default:
      // Sets and streams have no identity-addressed value to rewrite.
      return null;
  }
}
