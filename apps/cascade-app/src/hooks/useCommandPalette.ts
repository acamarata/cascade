/**
 * Purpose: Manages command palette open/close state and registers the global Cmd+K / Ctrl+K
 *   keyboard shortcut that toggles the palette from anywhere in the app.
 * Inputs:  None — purely internal state.
 * Outputs: { isOpen, open, close, toggle }
 * Constraints: Listener is on document so it fires regardless of which element has focus.
 *   Cleanup runs on unmount to prevent memory leaks / duplicate listeners.
 * SPORT: MASTER-HOOKS.md — useCommandPalette, src/hooks/useCommandPalette.ts
 */

import { useCallback, useEffect, useState } from 'react'

export function useCommandPalette() {
  const [isOpen, setIsOpen] = useState(false)
  const open = useCallback(() => setIsOpen(true), [])
  const close = useCallback(() => setIsOpen(false), [])
  const toggle = useCallback(() => setIsOpen((v) => !v), [])

  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      // Cmd+K on macOS, Ctrl+K on Windows/Linux
      const isMac = navigator.platform.toLowerCase().includes('mac')
      const triggered = isMac ? e.metaKey && e.key === 'k' : e.ctrlKey && e.key === 'k'
      if (triggered) {
        e.preventDefault()
        toggle()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [toggle])

  return { isOpen, open, close, toggle }
}
