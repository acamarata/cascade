/**
 * Purpose: Combined Zustand store — merges daemon, theme, and window slices using
 *   Immer middleware for safe immutable updates.
 * Inputs:  Slice creators from daemon.slice.ts, theme.slice.ts, window.slice.ts.
 * Outputs: useAppStore hook; convenience selector hooks re-exported.
 * Constraints: Immer middleware wraps the full store so every slice's set() receives
 *   an Immer draft — mutations are safe inside set() callbacks. Do not use produce()
 *   directly inside slice creators.
 * SPORT: MASTER-HOOKS.md — useAppStore, src/store/index.ts
 */

import { create } from 'zustand'
import { immer } from 'zustand/middleware/immer'

import { createDaemonSlice } from './daemon.slice'
import type { DaemonSlice } from './daemon.slice'
import { createThemeSlice } from './theme.slice'
import type { ThemeSlice } from './theme.slice'
import { createWindowSlice } from './window.slice'
import type { WindowSlice } from './window.slice'

export type AppStore = DaemonSlice & ThemeSlice & WindowSlice

export const useAppStore = create<AppStore>()(
  immer((...a) => ({
    ...createDaemonSlice(...a),
    ...createThemeSlice(...a),
    ...createWindowSlice(...a),
  }))
)

// ── Typed selector hooks ──────────────────────────────────────────────────────

/** Select daemon connection state + last status data. */
export function useDaemonStatus() {
  return useAppStore((s) => ({
    status: s.status,
    data: s.data,
    lastPing: s.lastPing,
    error: s.error,
    startPolling: s.startPolling,
    stopPolling: s.stopPolling,
  }))
}

/** Select theme token + resolved theme + setTheme action. */
export function useThemeStore() {
  return useAppStore((s) => ({
    token: s.token,
    resolvedTheme: s.resolvedTheme,
    setTheme: s.setTheme,
  }))
}

/** Select window state + open/close/focus actions. */
export function useWindowState() {
  return useAppStore((s) => ({
    openWindows: s.openWindows,
    activeWindow: s.activeWindow,
    openWindow: s.openWindow,
    closeWindow: s.closeWindow,
    focusWindow: s.focusWindow,
  }))
}
