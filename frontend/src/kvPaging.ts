import type { KVValue } from './types';

// Merging one continuation window into what the value panel already shows
// (P3-3 windowing). The row identity survives the append by construction: a
// list window carries the item's real rank in its index column, a hash edits by
// field, a zset by member; sets and streams are read-only. So the merged order
// is window-after-window (like Redis clients' own "load more"), and no edit can
// land on another element because the list shifted.

export const mergeKVWindow = (shown: KVValue, more: KVValue): KVValue => {
  const rows = [...(shown.result.rows ?? []), ...(more.result.rows ?? [])];
  return {
    ...more,
    result: {
      ...more.result,
      rows,
      rowCount: rows.length,
      // The grid's "cut" banner must follow the server's word: a window ends
      // either at a cursor or at the end of the collection.
      truncated: !more.done,
    },
  };
};
