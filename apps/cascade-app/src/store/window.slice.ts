/**
 * Purpose: Window state slice — tracks open secondary window labels and the active
 *   (focused) window label. Wires Tauri multi-window IPC (T-P3-E01-17) so open/
 *   close/focus actions drive real OS windows in addition to updating local state.
 * Inputs:  Window label strings (arbitrary Tauri window labels); url + title for open.
 * Outputs: WindowSlice — openWindows list, activeWindow, and mutation actions.
 * Constraints: openWindow now requires url + title to pass to the Tauri backend.
 *   IPC calls are fire-and-forget (errors logged to console, not surfaced to UI here).
 *   'main' is the implicit root window — never closed by this slice.
 * SPORT: MASTER-HOOKS.md — useWindowState, src/store/window.slice.ts
 */

import type { StateCreator } from 'zustand'
import * as windowsIpc from '../lib/windows'

export interface WindowSlice {
  /** Labels of currently open secondary windows (excludes 'main'). */
  openWindows: string[]
  /** Label of the currently active/focused window. Defaults to 'main'. */
  activeWindow: string
  /**
   * Open (or focus) a secondary Tauri window and register it in state.
   * @param label - Unique Tauri window label.
   * @param url   - App route to load (e.g. "/settings").
   * @param title - Window title bar text.
   */
  openWindow: (label: string, url: string, title: string) => void
  /** Close a secondary window by label; remove from open list. */
  closeWindow: (label: string) => void
  /** Focus a window by label; update activeWindow. */
  focusWindow: (label: string) => void
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const createWindowSlice: StateCreator<any, [['zustand/immer', never]], [], WindowSlice> = (
  set
) => ({
  openWindows: [],
  activeWindow: 'main',

  openWindow: (label: string, url: string, title: string) => {
    // Fire-and-forget IPC — check-or-focus handled Rust-side
    windowsIpc.openWindow(label, url, title).catch(console.error)
    set((draft: WindowSlice) => {
      if (!draft.openWindows.includes(label)) {
        draft.openWindows.push(label)
      }
      draft.activeWindow = label
    })
  },

  closeWindow: (label: string) => {
    windowsIpc.closeWindow(label).catch(console.error)
    set((draft: WindowSlice) => {
      draft.openWindows = draft.openWindows.filter((w: string) => w !== label)
      if (draft.activeWindow === label) {
        draft.activeWindow = 'main'
      }
    })
  },

  focusWindow: (label: string) => {
    windowsIpc.focusWindow(label).catch(console.error)
    set((draft: WindowSlice) => {
      draft.activeWindow = label
    })
  },
})
