import React, { useRef } from 'react';
import { FiX } from 'react-icons/fi';
import { useDialog } from '../dialog';

interface ShortcutHelpProps {
  onClose: () => void;
}

interface Shortcut {
  keys: string[];
  label: string;
}

// Every entry here is wired up in App's global handler or in the widget named;
// a shortcut listed but not bound is worse than one missing.
const shortcuts: { group: string; items: Shortcut[] }[] = [
  {
    group: '全局',
    items: [
      { keys: ['Ctrl / ⌘', 'P'], label: '快速打开表 / 视图' },
      { keys: ['Ctrl / ⌘', 'T'], label: '新建查询标签' },
      { keys: ['Ctrl / ⌘', 'W'], label: '关闭当前标签' },
      { keys: ['Ctrl / ⌘', '1…9'], label: '跳到第 N 个标签' },
      { keys: ['F1'], label: '打开 / 关闭本帮助（Ctrl / ⌘ + / 同效）' },
      { keys: ['Esc'], label: '关闭弹层' },
    ],
  },
  {
    group: '编辑器',
    items: [
      { keys: ['Ctrl / ⌘', 'Enter'], label: '执行当前语句' },
      { keys: ['Ctrl', 'Space'], label: '补全关键字 / 表 / 列' },
      { keys: ['Tab'], label: '采纳补全' },
    ],
  },
  {
    group: '结果网格',
    items: [
      { keys: ['双击单元格'], label: '编辑该值（可写回的结果）' },
      { keys: ['右键列头'], label: '排序 / 筛选该列' },
      { keys: ['Enter'], label: '确认单元格改动' },
      { keys: ['Esc'], label: '放弃单元格改动' },
      { keys: ['双击列宽把手'], label: '恢复该列自动宽度' },
    ],
  },
];

const ShortcutHelp: React.FC<ShortcutHelpProps> = ({ onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);

  return (
    <div className="fixed inset-0 z-50 bg-scrim/40 flex items-center justify-center p-6" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="shortcut-help-title"
        className="w-full max-w-2xl bg-panel border border-line-strong rounded-lg shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <header className="flex items-center px-4 py-2.5 border-b border-line">
          <h2 id="shortcut-help-title" className="text-sm font-semibold">快捷键</h2>
          <button className="ml-auto text-secondary hover:text-ink" onClick={onClose} aria-label="关闭快捷键说明">
            <FiX size={16} />
          </button>
        </header>

        <div className="px-4 py-3 grid grid-cols-2 gap-x-8 gap-y-4 max-h-[65vh] overflow-auto">
          {shortcuts.map(section => (
            <div key={section.group}>
              <h3 className="text-2xs font-semibold text-secondary uppercase tracking-wide mb-1.5">
                {section.group}
              </h3>
              <ul className="space-y-1.5">
                {section.items.map(item => (
                  <li key={item.label} className="flex items-center gap-2 text-[0.75rem]">
                    <span className="flex items-center gap-1 shrink-0">
                      {item.keys.map(key => (
                        <kbd
                          key={key}
                          className="font-mono text-2xs text-ink bg-raised border border-line-strong rounded px-1.5 py-0.5"
                        >
                          {key}
                        </kbd>
                      ))}
                    </span>
                    <span className="text-secondary truncate">{item.label}</span>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
};

export default ShortcutHelp;
