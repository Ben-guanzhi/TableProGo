// W3-c: the app's keyboard map as data.
//
// Two problems this solves. First, "a shortcut listed but not bound is worse
// than one missing" — the help panel used to be a hand-written list beside a
// hand-written handler, so the two could drift. Second, a duplicate binding
// silently loses to whichever handler registered last; that is invisible at
// runtime and obvious at startup if you assert on it.
//
// The table is the single source for both the global handler (App matches a
// key event against it) and the help panel (it renders the groups). Editing one
// place cannot make the two disagree.

export type ShortcutGroup = '全局' | '编辑器' | '结果网格' | '魔法命令';

export interface ShortcutSpec {
  // id is stable across relabelling and is what a handler looks up.
  id: string;
  group: ShortcutGroup;
  // keys is the display form, one entry per key cap: ['Ctrl / ⌘', 'P'].
  keys: string[];
  label: string;
  // combo is the dispatch form ('mod+p', 'f1', 'mod+shift+f'). Absent for
  // documentation-only rows (mouse gestures, slash commands) — those are not
  // bound by the global handler and must not take part in conflict checking.
  combo?: string;
}

// SHORTCUTS is the whole table. Order is the help panel's order.
export const SHORTCUTS: readonly ShortcutSpec[] = [
  // ---- global ----
  { id: 'quickOpen', group: '全局', keys: ['Ctrl / ⌘', 'P'], label: '快速打开表 / 视图', combo: 'mod+p' },
  { id: 'newTab', group: '全局', keys: ['Ctrl / ⌘', 'T'], label: '新建查询标签', combo: 'mod+t' },
  { id: 'closeTab', group: '全局', keys: ['Ctrl / ⌘', 'W'], label: '关闭当前标签', combo: 'mod+w' },
  { id: 'switchTab', group: '全局', keys: ['Ctrl / ⌘', '1…9'], label: '跳到第 N 个标签', combo: 'mod+digit' },
  { id: 'help', group: '全局', keys: ['F1'], label: '打开 / 关闭快捷键帮助', combo: 'f1' },
  { id: 'helpAlt', group: '全局', keys: ['Ctrl / ⌘', '/'], label: '同上（备用键位）', combo: 'mod+/' },
  { id: 'privacy', group: '全局', keys: ['Ctrl / ⌘', 'Shift', 'H'], label: '隐私模式（模糊连接名与数据）', combo: 'mod+shift+h' },
  { id: 'escape', group: '全局', keys: ['Esc'], label: '关闭弹层', combo: 'escape' },

  // ---- editor ----
  { id: 'run', group: '编辑器', keys: ['Ctrl / ⌘', 'Enter'], label: '执行当前语句（双栏时为活动栏）', combo: 'mod+enter' },
  { id: 'format', group: '编辑器', keys: ['Ctrl / ⌘', 'Shift', 'F'], label: '按当前预设格式化 SQL', combo: 'mod+shift+f' },
  { id: 'complete', group: '编辑器', keys: ['Ctrl', 'Space'], label: '补全关键字 / 表 / 列', combo: 'ctrl+space' },
  { id: 'accept', group: '编辑器', keys: ['Tab'], label: '采纳补全' },

  // ---- multi-cursor (W3-a) ----
  { id: 'cursorAbove', group: '编辑器', keys: ['Ctrl / ⌘', 'Alt', '↑'], label: '在上方加一个光标', combo: 'mod+alt+arrowup' },
  { id: 'cursorBelow', group: '编辑器', keys: ['Ctrl / ⌘', 'Alt', '↓'], label: '在下方加一个光标', combo: 'mod+alt+arrowdown' },
  { id: 'nextMatch', group: '编辑器', keys: ['Ctrl / ⌘', 'D'], label: '选中下一个相同文本（多光标）', combo: 'mod+d' },
  { id: 'selectAllMatches', group: '编辑器', keys: ['Ctrl / ⌘', 'Shift', 'D'], label: '选中全部相同文本', combo: 'mod+shift+d' },

  // ---- grid ----
  { id: 'gridEdit', group: '结果网格', keys: ['双击单元格'], label: '编辑该值（可写回的结果）' },
  { id: 'gridColumnMenu', group: '结果网格', keys: ['右键列头'], label: '排序 / 筛选该列' },
  { id: 'gridCommit', group: '结果网格', keys: ['Enter'], label: '确认单元格改动' },
  { id: 'gridRevert', group: '结果网格', keys: ['Esc'], label: '放弃单元格改动' },
  { id: 'gridUndo', group: '结果网格', keys: ['Ctrl / ⌘', 'Z'], label: '撤销最近一次网格改动（焦点在网格时；焦点在编辑器则撤销文本）', combo: 'mod+z' },
  { id: 'gridAutoWidth', group: '结果网格', keys: ['双击列宽把手'], label: '恢复该列自动宽度' },

  // ---- magic commands (W1-a) ----
  { id: 'magic', group: '魔法命令', keys: ['\\ 开头'], label: '\\dt \\dv \\dn \\d \\di \\df \\copy \\? —— 拦截为结果网格，不作为 SQL 执行' },
];

// groupOrder fixes the help panel's section order regardless of table order.
export const SHORTCUT_GROUPS: readonly ShortcutGroup[] = ['全局', '编辑器', '结果网格', '魔法命令'];

// shortcutsByGroup groups the table for rendering.
export const shortcutsByGroup = (): { group: ShortcutGroup; items: ShortcutSpec[] }[] =>
  SHORTCUT_GROUPS.map(group => ({
    group,
    items: SHORTCUTS.filter(s => s.group === group),
  }));

// normalizeCombo folds the spelling of a key event into the table's combo form.
// It is the one place that knows "Cmd on mac, Ctrl elsewhere" and how modifier
// order is written, so a handler and the table can never disagree about what
// `mod+shift+f` means.
//
// `ctrl` (literal Control, any platform) is distinct from `mod`: Ctrl+Space is
// CodeMirror's completion binding and stays Control on macOS too, so folding it
// into mod would make it Cmd+Space there — the Spotlight shortcut.
export const normalizeCombo = (event: {
  key: string;
  ctrlKey?: boolean;
  metaKey?: boolean;
  altKey?: boolean;
  shiftKey?: boolean;
  isMac?: boolean;
}): string => {
  const isMac = !!event.isMac;
  // On macOS, Control is a literal modifier (mod is Cmd there). Elsewhere mod
  // IS ctrl, so it is emitted once, as mod.
  const ctrl = !!event.ctrlKey;
  const mod = isMac ? !!event.metaKey : ctrl;
  const parts: string[] = [];
  if (mod) parts.push('mod');
  else if (ctrl) parts.push('ctrl');
  if (!isMac && !!event.metaKey) parts.push('meta');
  if (event.altKey) parts.push('alt');
  if (event.shiftKey) parts.push('shift');
  const raw = event.key;
  const key = raw === ' ' ? 'space' : raw === 'ArrowUp' ? 'arrowup'
    : raw === 'ArrowDown' ? 'arrowdown' : raw === 'Escape' ? 'escape'
    : raw === 'Enter' ? 'enter' : raw.length === 1 ? raw.toLowerCase() : raw.toLowerCase();
  parts.push(key);
  return parts.join('+');
};

// comboMatches reports whether a normalized combo satisfies a table combo.
// `mod+digit` stands for any of mod+1 … mod+9; everything else is exact. The
// two-way rule lives here so a caller never has to special-case the digits.
export const comboMatches = (spec: string, combo: string): boolean => {
  if (spec === combo) return true;
  if (spec === 'mod+digit') return /^mod\+[1-9]$/.test(combo);
  return false;
};

// findShortcut resolves one normalized combo to the table entry that owns it.
// The first match wins, which is why duplicates are a startup error rather than
// a silent shadow.
export const findShortcut = (combo: string): ShortcutSpec | null =>
  SHORTCUTS.find(s => s.combo && comboMatches(s.combo, combo)) ?? null;

// duplicateCombos lists combos declared by more than one bindable row. App
// asserts this at startup (and a test pins it): two handlers claiming one combo
// means one of them silently never fires.
export const duplicateCombos = (): string[] => {
  const seen = new Map<string, number>();
  for (const s of SHORTCUTS) {
    if (!s.combo) continue;
    seen.set(s.combo, (seen.get(s.combo) ?? 0) + 1);
  }
  return [...seen.entries()].filter(([, n]) => n > 1).map(([combo]) => combo);
};
