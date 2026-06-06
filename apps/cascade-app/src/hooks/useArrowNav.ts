/**
 * Purpose: Roving focus within a list container — arrow keys move focus between
 *   focusable children, Home/End jump to first/last, Escape blurs the container.
 * Inputs:  containerRef — ref to the <ul>/<div> containing focusable items.
 * Outputs: void (attaches/detaches keydown listener as a side-effect).
 * Constraints: Only ArrowDown/ArrowUp/Home/End/Escape are handled; all other keys
 *   propagate normally. Respects prefers-reduced-motion (no animation involved).
 *   Cleanup runs on unmount — no memory leaks.
 * SPORT: MASTER-HOOKS.md — useArrowNav
 */

import { useEffect } from 'react';

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function useArrowNav(containerRef: React.RefObject<HTMLElement | null>) {
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    function getFocusable(): HTMLElement[] {
      return Array.from(el!.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
    }

    function handleKeyDown(e: KeyboardEvent) {
      const items = getFocusable();
      if (items.length === 0) return;

      const active = document.activeElement as HTMLElement;
      const idx = items.indexOf(active);

      switch (e.key) {
        case 'ArrowDown': {
          e.preventDefault();
          const next = idx < items.length - 1 ? items[idx + 1] : items[0];
          next.focus();
          break;
        }
        case 'ArrowUp': {
          e.preventDefault();
          const prev = idx > 0 ? items[idx - 1] : items[items.length - 1];
          prev.focus();
          break;
        }
        case 'Home': {
          e.preventDefault();
          items[0].focus();
          break;
        }
        case 'End': {
          e.preventDefault();
          items[items.length - 1].focus();
          break;
        }
        case 'Escape': {
          active.blur();
          break;
        }
        default:
          break;
      }
    }

    el.addEventListener('keydown', handleKeyDown);
    return () => el.removeEventListener('keydown', handleKeyDown);
  }, [containerRef]);
}
