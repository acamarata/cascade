/**
 * Purpose: Manages open/close state of the command palette.
 * Outputs: { isOpen, open, close, toggle }
 * SPORT: MASTER-HOOKS.md — useCommandPalette
 */

import { useCallback, useState } from "react";

export function useCommandPalette() {
  const [isOpen, setIsOpen] = useState(false);
  const open = useCallback(() => setIsOpen(true), []);
  const close = useCallback(() => setIsOpen(false), []);
  const toggle = useCallback(() => setIsOpen((v) => !v), []);
  return { isOpen, open, close, toggle };
}
