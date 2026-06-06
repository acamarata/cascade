/**
 * Purpose: Window state slice — tracks open secondary window labels and the active
 *   (focused) window label. Provides open/close/focus actions.
 * Inputs:  Window label strings (arbitrary Tauri window labels).
 * Outputs: WindowSlice — openWindows list, activeWindow, and mutation actions.
 * Constraints: Does NOT call Tauri multi-window APIs (that is T-P3-E01-18). This
 *   slice is purely client-side state tracking. 'main' is the implicit root window.
 * SPORT: MASTER-HOOKS.md — useWindowState, src/store/window.slice.ts
 */

import type { StateCreator } from 'zustand';

export interface WindowSlice {
  /** Labels of currently open secondary windows (excludes 'main'). */
  openWindows: string[];
  /** Label of the currently active/focused window. Defaults to 'main'. */
  activeWindow: string;
  /** Register a window as open and focus it. */
  openWindow: (label: string) => void;
  /** Remove a window from the open list. Resets activeWindow to 'main' if it was focused. */
  closeWindow: (label: string) => void;
  /** Set the active window without changing the open list. */
  focusWindow: (label: string) => void;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const createWindowSlice: StateCreator<any, [['zustand/immer', never]], [], WindowSlice> = (set) => ({
  openWindows: [],
  activeWindow: 'main',

  openWindow: (label: string) => {
    set((draft: WindowSlice) => {
      if (!draft.openWindows.includes(label)) {
        draft.openWindows.push(label);
      }
      draft.activeWindow = label;
    });
  },

  closeWindow: (label: string) => {
    set((draft: WindowSlice) => {
      draft.openWindows = draft.openWindows.filter((w: string) => w !== label);
      if (draft.activeWindow === label) {
        draft.activeWindow = 'main';
      }
    });
  },

  focusWindow: (label: string) => {
    set((draft: WindowSlice) => {
      draft.activeWindow = label;
    });
  },
});
