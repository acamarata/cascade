/**
 * Purpose: Theme state slice — tracks theme token, resolved theme, and provides setTheme
 *   action. Reads from and writes to localStorage key 'cascade-theme' for persistence.
 * Inputs:  localStorage.getItem('cascade-theme') on initialisation; window.matchMedia for
 *   system preference resolution when token is 'system'.
 * Outputs: ThemeSlice — token, resolved (always 'dark'|'light'), setTheme action.
 * Constraints: localStorage may be unavailable in SSR contexts — guarded with try/catch.
 *   resolvedTheme is computed at read time, not stored, to stay in sync with system changes.
 * SPORT: MASTER-HOOKS.md — useTheme, src/store/theme.slice.ts
 */

import type { StateCreator } from 'zustand';

export type ThemeToken = 'light' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'dark';

const STORAGE_KEY = 'cascade-theme';

function readStoredTheme(): ThemeToken {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === 'light' || stored === 'dark' || stored === 'system') {
      return stored;
    }
  } catch {
    // localStorage unavailable (e.g. sandboxed context).
  }
  return 'system';
}

function resolveTheme(token: ThemeToken): ResolvedTheme {
  if (token !== 'system') return token;
  try {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  } catch {
    return 'light';
  }
}

export interface ThemeSlice {
  /** User-selected theme token. */
  token: ThemeToken;
  /** Resolved concrete theme (never 'system'). */
  resolvedTheme: ResolvedTheme;
  /** Update the theme token and persist to localStorage. */
  setTheme: (t: ThemeToken) => void;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export const createThemeSlice: StateCreator<any, [['zustand/immer', never]], [], ThemeSlice> = (set) => {
  const initialToken = readStoredTheme();
  return {
    token: initialToken,
    resolvedTheme: resolveTheme(initialToken),

    setTheme: (t: ThemeToken) => {
      try {
        localStorage.setItem(STORAGE_KEY, t);
      } catch {
        // Ignore write failure.
      }
      set((draft: ThemeSlice) => {
        draft.token = t;
        draft.resolvedTheme = resolveTheme(t);
      });
    },
  };
};
