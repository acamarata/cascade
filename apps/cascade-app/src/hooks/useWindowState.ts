/**
 * Purpose: Typed selector hook for secondary window state. Re-exports from store/.
 * Inputs:  None.
 * Outputs: { openWindows, activeWindow, openWindow, closeWindow, focusWindow }
 * Constraints: Delegates entirely to useWindowState from src/store/index.ts.
 * SPORT: MASTER-HOOKS.md — useWindowState, src/hooks/useWindowState.ts
 */

export { useWindowState } from '../store'
