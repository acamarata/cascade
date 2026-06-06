/**
 * Purpose: Re-exports useThemeStore as useTheme for backwards-compat import paths.
 *   All theme state now lives in the Zustand store; Tauri IPC is no longer used.
 * Inputs:  None.
 * Outputs: { token, resolvedTheme, setTheme } — same as useThemeStore.
 * Constraints: Do not add logic here. This is a pure re-export shim.
 * SPORT: MASTER-HOOKS.md — useTheme
 */

export { useThemeStore as useTheme } from '../store/index';
