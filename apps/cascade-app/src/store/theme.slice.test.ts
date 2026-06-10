/**
 * Purpose: Unit tests for themeSlice — initial read from localStorage, setTheme persists.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { create } from 'zustand'
import { immer } from 'zustand/middleware/immer'

import { createThemeSlice } from './theme.slice'
import type { ThemeSlice } from './theme.slice'

function makeStore() {
  return create<ThemeSlice>()(immer((...a) => createThemeSlice(...a)))
}

// Simple in-memory localStorage stub for environments where it may not be available.
const localStorageData: Record<string, string> = {}
const localStorageMock = {
  getItem: (key: string) => localStorageData[key] ?? null,
  setItem: (key: string, value: string) => {
    localStorageData[key] = value
  },
  removeItem: (key: string) => {
    delete localStorageData[key]
  },
  clear: () => {
    for (const k in localStorageData) delete localStorageData[k]
  },
}

vi.stubGlobal('localStorage', localStorageMock)

describe('themeSlice', () => {
  beforeEach(() => {
    localStorageMock.clear()
  })

  it('defaults to system when no localStorage entry', () => {
    const store = makeStore()
    expect(store.getState().token).toBe('system')
  })

  it('reads stored token from localStorage on init', () => {
    localStorage.setItem('cascade-theme', 'dark')
    const store = makeStore()
    expect(store.getState().token).toBe('dark')
    expect(store.getState().resolvedTheme).toBe('dark')
  })

  it('setTheme writes to localStorage and updates state', () => {
    const store = makeStore()
    store.getState().setTheme('light')
    expect(store.getState().token).toBe('light')
    expect(store.getState().resolvedTheme).toBe('light')
    expect(localStorage.getItem('cascade-theme')).toBe('light')
  })
})
