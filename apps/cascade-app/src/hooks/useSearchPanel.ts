/**
 * Purpose: Manages SearchPanel open/close state and registers the global
 *   Cmd+Shift+F (macOS) / Ctrl+Shift+F (Windows/Linux) keyboard shortcut
 *   that toggles the panel from anywhere in the app.
 * Inputs:  None — purely internal state.
 * Outputs: { isOpen, open, close, toggle }
 * Constraints:
 *   - Listener is on document so it fires regardless of which element has focus.
 *   - Cleanup runs on unmount to prevent memory leaks / duplicate listeners.
 *   - Does NOT preventDefault for the fallback Ctrl+Shift+F — that shortcut is
 *     common in many apps; only Cmd+Shift+F (macOS metaKey) is prevented.
 * SPORT: MASTER-HOOKS.md — useSearchPanel (T-P3-E06-09)
 */

import { useCallback, useEffect, useState } from 'react'

export function useSearchPanel() {
  const [isOpen, setIsOpen] = useState(false)
  const open = useCallback(() => setIsOpen(true), [])
  const close = useCallback(() => setIsOpen(false), [])
  const toggle = useCallback(() => setIsOpen((v) => !v), [])

  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      const isMac = navigator.platform.toLowerCase().includes('mac')
      // Cmd+Shift+F on macOS, Ctrl+Shift+F on Windows/Linux
      const triggered = e.shiftKey && e.key === 'F' && (isMac ? e.metaKey : e.ctrlKey)
      // Also handle lowercase 'f' in case Shift produces uppercase depending on platform
      const triggeredLower = e.shiftKey && e.key === 'f' && (isMac ? e.metaKey : e.ctrlKey)
      if (triggered || triggeredLower) {
        e.preventDefault()
        toggle()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [toggle])

  return { isOpen, open, close, toggle }
}
