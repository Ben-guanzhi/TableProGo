import { useEffect, useRef } from 'react';
import type { RefObject } from 'react';

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

const visible = (el: HTMLElement) => el.offsetWidth > 0 || el.offsetHeight > 0 || el === document.activeElement;

export const focusableIn = (panel: HTMLElement): HTMLElement[] =>
  Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(visible);

// Where Tab lands next, wrapping at both ends so focus cannot leak to the
// panels behind a dialog.
export const nextFocusable = (
  list: HTMLElement[],
  from: HTMLElement | null,
  shift: boolean,
): HTMLElement | null => {
  if (list.length === 0) return null;
  const at = from ? list.indexOf(from) : -1;
  if (at === -1) return shift ? list[list.length - 1] : list[0];
  return list[(at + (shift ? -1 : 1) + list.length) % list.length];
};

// Where focus goes when a dialog opens: the field the author pointed at, else
// the first field, else the first control. Landing on a close button instead of
// the name you are about to type is a bad opening move.
export const initialFocus = (panel: HTMLElement): HTMLElement | null =>
  panel.querySelector<HTMLElement>('[autofocus]') ??
  panel.querySelector<HTMLElement>('input:not([type="hidden"]), select, textarea') ??
  focusableIn(panel)[0] ??
  null;

// Escape, the focus trap and handing focus back on the way out - the three
// things a floating div has to do to behave like a dialog.
export function useDialog(panelRef: RefObject<HTMLElement>, onClose: () => void) {
  // Callers pass a fresh arrow on every render; holding it in a ref keeps the
  // effect to a single mount, so typing in the dialog cannot re-run the
  // initial focus.
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const previousRef = useRef<HTMLElement | null>(document.activeElement as HTMLElement | null);

  useEffect(() => {
    const panel = panelRef.current;
    // Read during the first render: by the time an effect runs, a field with
    // `autoFocus` has already moved the focus inside the dialog, and the
    // element it came from would be forgotten.
    const previous = previousRef.current;
    if (panel && !panel.contains(document.activeElement)) initialFocus(panel)?.focus();

    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        // The dialog is on top, so nothing behind it gets the same keypress.
        event.stopPropagation();
        closeRef.current();
        return;
      }
      if (event.key !== 'Tab' || !panel) return;
      const next = nextFocusable(focusableIn(panel), document.activeElement as HTMLElement, event.shiftKey);
      if (next) {
        event.preventDefault();
        next.focus();
      }
    };
    window.addEventListener('keydown', onKey, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      previous?.focus?.();
    };
  }, [panelRef]);
}
