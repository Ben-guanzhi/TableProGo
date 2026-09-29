import { Theme } from './api';

const APPLIED_KEY = 'tablepro.theme';

export function resolveTheme(theme: Theme): 'light' | 'dark' {
  if (theme === 'light' || theme === 'dark') return theme;
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function applyTheme(theme: Theme) {
  const applied = resolveTheme(theme);
  document.documentElement.dataset.theme = applied;
  try {
    localStorage.setItem(APPLIED_KEY, applied);
  } catch {
    // Storage can be unavailable; the theme still applies for this run.
  }
}

// Called before the first render: settings arrive asynchronously, and without
// this a light-theme user would see the dark palette flash first.
export function preapplyTheme() {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(APPLIED_KEY);
  } catch {
    return;
  }
  if (stored === 'light' || stored === 'dark') {
    document.documentElement.dataset.theme = stored;
  }
}
