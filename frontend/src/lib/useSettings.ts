import { useEffect } from 'react';
import { AppSettings, GetSettings, SaveSettings } from '../api';
import { applyTheme } from '../theme';

// App settings: the defaults, the one-way save, and the three effects that keep
// the OS theme, the CSS variables and the persisted choice in sync.
//
// Split out of App.tsx because settings touch three different stores (state, the
// backend file, the OS preference) and each has its own rule; keeping them in one
// hook makes "why did the theme change" answerable in one place.

export const defaultSettings: AppSettings = {
  showColumnComments: false,
  resultLimit: 1000,
  theme: 'system',
  safeMode: 'off',
};

// useSettingsEffect paints the theme and follows the OS preference while the
// choice is "system". Applying is a DOM write, so it happens in an effect rather
// than during render.
export function useSettingsEffect(settings: AppSettings) {
  useEffect(() => {
    applyTheme(settings.theme);
    if (settings.theme !== 'system' || typeof window.matchMedia !== 'function') return;
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = () => applyTheme('system');
    media.addEventListener('change', onChange);
    return () => media.removeEventListener('change', onChange);
  }, [settings.theme]);
}

// useLoadSettings reads the persisted settings once at startup.
export function useLoadSettings(onLoaded: (settings: AppSettings) => void) {
  useEffect(() => {
    GetSettings()
      .then(s => onLoaded({ ...defaultSettings, ...(s ?? {}) }))
      .catch(() => {});
    // Once, at startup: the callback identity is not a dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
}

// saveSettings writes the choice through, logging instead of throwing: a failed
// save must not interrupt the interaction that changed the setting.
export function saveSettings(next: AppSettings) {
  SaveSettings(next).catch(error => console.error('Failed to save settings:', error));
}
