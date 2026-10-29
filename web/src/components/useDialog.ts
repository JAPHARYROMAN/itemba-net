import { useEffect, useRef } from 'react';

// Shared behaviour for the console's modal dialogs: Escape closes, Tab stays
// inside, the first control takes focus, and the trigger gets it back when
// the dialog unmounts. The close handler lives in a ref so an inline arrow
// from the parent does not restart the effect and pull focus away.
export const useDialog = <T extends HTMLElement>(onClose: () => void) => {
  const ref = useRef<T | null>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    const node = ref.current;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const focusable = () => Array.from(node?.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ) ?? []);
    (focusable()[0] ?? node)?.focus();

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        closeRef.current();
        return;
      }
      if (event.key !== 'Tab') return;
      const items = focusable();
      if (items.length === 0) return;
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (event.shiftKey && (active === first || !node?.contains(active))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (active === last || !node?.contains(active))) {
        event.preventDefault();
        first.focus();
      }
    };

    // The page behind the dialog must not scroll under it. The stable gutter
    // in styles.css keeps the layout from jumping when the bar is removed.
    const bodyOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      document.body.style.overflow = bodyOverflow;
      if (previous && document.contains(previous)) previous.focus();
    };
  }, []);

  return ref;
};
