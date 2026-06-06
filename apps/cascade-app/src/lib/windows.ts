/**
 * Purpose: Typed TypeScript wrappers for Tauri multi-window IPC commands.
 * Inputs:  label — Tauri window label; url — app route (e.g. "/settings");
 *          title — window title bar text.
 * Outputs: void promises; errors propagate as CascadeIpcError from the Rust side.
 * Constraints: Wraps cascade_open_window / cascade_close_window / cascade_focus_window.
 *   openWindow uses check-or-focus — safe to call repeatedly with the same label.
 * SPORT: MASTER-LIBS.md — windows IPC helpers, src/lib/windows.ts
 */

import { invoke } from '@tauri-apps/api/core';

/**
 * Open a secondary Tauri window at `url` with `title`, or focus it if already open.
 *
 * @param label - Unique Tauri window label (e.g. "settings-panel").
 * @param url   - App route to load (e.g. "/settings").
 * @param title - Window title bar text.
 */
export async function openWindow(label: string, url: string, title: string): Promise<void> {
  return invoke('cascade_open_window', { label, url, title });
}

/**
 * Close a secondary window by label. Silent no-op if the window is not open.
 *
 * @param label - Tauri window label to close.
 */
export async function closeWindow(label: string): Promise<void> {
  return invoke('cascade_close_window', { label });
}

/**
 * Focus an existing secondary window by label. Silent no-op if not open.
 *
 * @param label - Tauri window label to bring to front.
 */
export async function focusWindow(label: string): Promise<void> {
  return invoke('cascade_focus_window', { label });
}
