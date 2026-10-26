// Open a URL in the user's real browser. Inside the Wails webview that is
// Browser.OpenURL; the vite dev server runs in a plain browser where the call
// is unavailable, so fall back to window.open. Never navigate the app itself
// away from the client.

import { Browser } from '@wailsio/runtime';

export async function openExternal(url: string): Promise<void> {
  try {
    await Browser.OpenURL(url);
  } catch {
    window.open(url, '_blank', 'noopener,noreferrer');
  }
}
