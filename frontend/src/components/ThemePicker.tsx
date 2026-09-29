import React from 'react';
import { FiChevronDown, FiMonitor, FiMoon, FiSun } from 'react-icons/fi';
import { Theme } from '../api';

const labels: Record<Theme, string> = {
  system: '跟随系统',
  light: '浅色',
  dark: '深色',
};

const ThemePicker: React.FC<{ theme: Theme; onChange: (theme: Theme) => void }> = ({
  theme,
  onChange,
}) => {
  const Icon = theme === 'dark' ? FiMoon : theme === 'light' ? FiSun : FiMonitor;

  return (
    <div className="relative flex items-center">
      <Icon size={11} className="absolute left-2 text-muted pointer-events-none" />
      <FiChevronDown size={11} className="absolute right-1.5 text-muted pointer-events-none" />
      <select
        className="appearance-none bg-canvas hover:bg-raised border border-line rounded pl-6 pr-6 py-1 text-xs text-ink cursor-pointer focus:outline-none focus:border-accent"
        value={theme}
        onChange={e => onChange(e.target.value as Theme)}
        aria-label="界面主题"
        title="界面主题：跟随系统会随系统的深浅色切换"
      >
        {(Object.keys(labels) as Theme[]).map(key => (
          <option key={key} value={key}>
            {labels[key]}
          </option>
        ))}
      </select>
    </div>
  );
};

export default ThemePicker;
