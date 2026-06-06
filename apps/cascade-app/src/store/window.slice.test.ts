/**
 * Purpose: Unit tests for windowSlice — open/close/focus actions.
 */

import { describe, it, expect } from 'vitest';
import { create } from 'zustand';
import { immer } from 'zustand/middleware/immer';

import { createWindowSlice } from './window.slice';
import type { WindowSlice } from './window.slice';

function makeStore() {
  return create<WindowSlice>()(immer((...a) => createWindowSlice(...a)));
}

describe('windowSlice', () => {
  it('initial state has empty openWindows and main active', () => {
    const store = makeStore();
    expect(store.getState().openWindows).toEqual([]);
    expect(store.getState().activeWindow).toBe('main');
  });

  it('openWindow adds label and sets activeWindow', () => {
    const store = makeStore();
    store.getState().openWindow('settings');
    expect(store.getState().openWindows).toContain('settings');
    expect(store.getState().activeWindow).toBe('settings');
  });

  it('openWindow is idempotent — does not duplicate labels', () => {
    const store = makeStore();
    store.getState().openWindow('settings');
    store.getState().openWindow('settings');
    expect(store.getState().openWindows.filter((w) => w === 'settings')).toHaveLength(1);
  });

  it('closeWindow removes label and resets activeWindow to main when focused', () => {
    const store = makeStore();
    store.getState().openWindow('settings');
    store.getState().closeWindow('settings');
    expect(store.getState().openWindows).not.toContain('settings');
    expect(store.getState().activeWindow).toBe('main');
  });

  it('closeWindow does not change activeWindow if a different window was focused', () => {
    const store = makeStore();
    store.getState().openWindow('settings');
    store.getState().openWindow('about');
    store.getState().closeWindow('settings');
    expect(store.getState().activeWindow).toBe('about');
  });

  it('focusWindow updates activeWindow without changing openWindows', () => {
    const store = makeStore();
    store.getState().openWindow('settings');
    store.getState().focusWindow('main');
    expect(store.getState().activeWindow).toBe('main');
    expect(store.getState().openWindows).toContain('settings');
  });
});
