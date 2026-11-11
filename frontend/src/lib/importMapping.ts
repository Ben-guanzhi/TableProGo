// Column mapping, the pure half of the import dialog: parse the mapping text,
// validate it against the file's columns and apply it. Kept free of React and of
// the API so it can be unit-tested directly.
//
// The backend applies the same rules (internal/importfile/mapping.go): an
// explicit mapping is authoritative, so a source it does not name is skipped.

export interface ColumnMapEntry {
  source: string;
  /** Empty means the source column is skipped. */
  target: string;
}

// parseMappingText reads the mapping editor's text: one assignment per line.
// `a = b`, `a => b` and `a: b` all mean "file column a goes to table column b",
// a line with no target skips that source, and blank lines plus `#` comments are
// ignored. When the same source appears twice the later line wins, which mirrors
// how a user would correct a typo in place.
export function parseMappingText(text: string): ColumnMapEntry[] {
  const order: string[] = [];
  const targets = new Map<string, string>();
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (line === '' || line.startsWith('#')) continue;
    const match = line.match(/^([^=:>]+?)\s*(?:=>|=|:)\s*(.*)$/);
    const source = (match ? match[1] : line).trim();
    const target = (match ? match[2] : '').trim();
    if (source === '') continue;
    if (!targets.has(source)) order.push(source);
    targets.set(source, target);
  }
  return order.map((source) => ({ source, target: targets.get(source) ?? '' }));
}

// mappingTextToEntries is parseMappingText with the file's own column order, so
// the editor shows every file column even when the text only names some of them.
export function mappingTextToEntries(text: string, fileCols: string[]): ColumnMapEntry[] {
  const bySource = new Map(parseMappingText(text).map((e) => [e.source, e.target]));
  return fileCols.map((source) => ({ source, target: bySource.get(source) ?? '' }));
}

// entriesToText renders entries back to the editor's text form.
export function entriesToText(entries: ColumnMapEntry[]): string {
  return entries
    .filter((e) => e.source !== '')
    .map((e) => (e.target === '' ? e.source : `${e.source} => ${e.target}`))
    .join('\n');
}

// validateMapping returns the human-readable problems with an explicit mapping,
// in the order the backend reports them. Empty means the mapping is usable.
export function validateMapping(entries: ColumnMapEntry[], fileCols: string[]): string[] {
  const problems: string[] = [];
  if (entries.length === 0) return problems;
  const known = new Set(fileCols);
  const targets = new Set<string>();
  let kept = 0;
  for (const [i, e] of entries.entries()) {
    if (!known.has(e.source)) {
      problems.push(`映射的源列 "${e.source}" 不在文件列中（第 ${i + 1} 条映射）`);
    }
    if (e.target === '') continue;
    if (targets.has(e.target)) {
      problems.push(`映射目标列 "${e.target}" 被使用了多次（第 ${i + 1} 条映射）`);
    }
    targets.add(e.target);
    kept++;
  }
  if (kept === 0 && problems.length === 0) {
    problems.push('所有源列都被跳过，没有可导入的列');
  }
  return problems;
}

// applyMapping turns the mapping into the backend's request shape: the kept
// targets in file order and the sources the mapping skips.
export function applyMapping(entries: ColumnMapEntry[], fileCols: string[]): {
  columnMap: Record<string, string>;
  skipColumns: string[];
  cols: string[];
  keep: number[];
} {
  const bySource = new Map(entries.map((e) => [e.source, e.target]));
  const columnMap: Record<string, string> = {};
  const skipColumns: string[] = [];
  const cols: string[] = [];
  const keep: number[] = [];
  fileCols.forEach((source, i) => {
    const target = bySource.get(source);
    if (target === undefined || target === '') {
      skipColumns.push(source);
      return;
    }
    columnMap[source] = target;
    cols.push(target);
    keep.push(i);
  });
  return { columnMap, skipColumns, cols, keep };
}

// parseSkipList reads the comma-separated "skip these source columns" input.
export function parseSkipList(text: string): string[] {
  return text
    .split(/[,，]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

// parsePathList reads the one path field as a list: a multi-file import lists
// one path per line (commas too, so the field also accepts "a.csv, b.csv").
export function parsePathList(text: string): string[] {
  return text
    .split(/[\n,，]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
