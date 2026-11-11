import React, { useRef } from 'react';
import { FiX } from 'react-icons/fi';
import { useDialog } from '../dialog';
import { shortcutsByGroup } from '../lib/shortcuts';

interface ShortcutHelpProps {
  onClose: () => void;
}

// W3-c: the panel renders `lib/shortcuts.ts` — the same table the global key
// handler dispatches on. A shortcut can no longer be listed here without being
// bound, which was the failure mode when the list was hand-written beside the
// handler.
const ShortcutHelp: React.FC<ShortcutHelpProps> = ({ onClose }) => {
  const panelRef = useRef<HTMLDivElement>(null);
  useDialog(panelRef, onClose);
  const groups = shortcutsByGroup();

  return (
    <div className="fixed inset-0 z-50 bg-scrim/40 flex items-center justify-center p-6" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="shortcut-help-title"
        className="w-full max-w-3xl bg-panel border border-line-strong rounded-lg shadow-xl"
        onClick={e => e.stopPropagation()}
      >
        <header className="flex items-center px-4 py-2.5 border-b border-line">
          <h2 id="shortcut-help-title" className="text-sm font-semibold">快捷键</h2>
          <button className="ml-auto text-secondary hover:text-ink" onClick={onClose} aria-label="关闭快捷键说明">
            <FiX size={16} />
          </button>
        </header>

        <div className="px-4 py-3 grid grid-cols-2 gap-x-8 gap-y-4 max-h-[65vh] overflow-auto">
          {groups.map(section => (
            <div key={section.group}>
              <h3 className="text-2xs font-semibold text-secondary uppercase tracking-wide mb-1.5">
                {section.group}
              </h3>
              <ul className="space-y-1.5">
                {section.items.map(item => (
                  <li key={item.id} className="flex items-start gap-2 text-[0.75rem]">
                    <span className="flex items-center gap-1 shrink-0">
                      {item.keys.map(key => (
                        <kbd
                          key={key}
                          className="px-1 py-0.5 text-2xs border border-line rounded bg-canvas text-secondary whitespace-nowrap"
                        >
                          {key}
                        </kbd>
                      ))}
                    </span>
                    <span className="text-ink min-w-0">{item.label}</span>
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
