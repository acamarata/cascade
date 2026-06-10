/**
 * Purpose: IPC hook for reading and writing CascadeSettings via get_settings /
 *   update_settings Tauri commands. Provides dirty-state tracking and per-section
 *   save/discard helpers consumed by all settings tabs.
 * Inputs:  None (reads on mount via Tauri invoke).
 * Outputs: { settings, isLoading, isDirty, save, discard, updateSection }
 * Constraints:
 *   - Uses partial-update pattern: only the mutated section is sent to update_settings.
 *   - Falls back to a sensible default when window.__TAURI__ is absent (browser/Vitest).
 *   - No circular re-renders: setDraft triggers a stable reference only when contents change.
 * SPORT: MASTER-LIBS.md — useSettings hook — src/features/settings/useSettings.ts (T-P3-E07-15)
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { CascadeSettings, SettingsUpdate } from '@/types/settings'

// ── Default / fallback ────────────────────────────────────────────────────────

/** Returns a fully-populated default settings object for browser/Vitest runs. */
export function defaultSettings(): CascadeSettings {
  return {
    schemaVersion: '2',
    library: { defaultHarnessTargets: [] },
    context: {},
    projectMap: {},
    providers: { google: [] },
    geminiPool: { keys: [], proxyPort: 3761, enabled: false },
    harnessBridges: {},
    hooks: [],
    scheduledTasks: [],
    plugins: { enabled: [], config: {} },
    widgets: { positions: {} },
    mcpServers: [],
    vaultDisplay: { showMasked: false },
    telemetry: { enabled: false },
  }
}

// ── Hook ──────────────────────────────────────────────────────────────────────

export interface UseSettingsResult {
  /** Current (saved) settings snapshot from disk. */
  settings: CascadeSettings | null
  /** Draft copy mutated by the form; differs from settings when isDirty. */
  draft: CascadeSettings | null
  isLoading: boolean
  /** True when draft differs from saved settings. */
  isDirty: boolean
  /** Call to update a single top-level section in the draft. */
  updateSection: <K extends keyof CascadeSettings>(section: K, value: CascadeSettings[K]) => void
  /** Persist the current draft section to disk via update_settings. */
  saveSection: <K extends keyof CascadeSettings>(section: K) => Promise<void>
  /** Revert draft to match saved settings. */
  discard: () => void
  /** Reload settings from disk (e.g. after external change). */
  reload: () => Promise<void>
  /** Error message if the last IPC call failed. */
  error: string | null
}

/** Shallow equality check for top-level section values. */
function sectionChanged<K extends keyof CascadeSettings>(
  a: CascadeSettings | null,
  b: CascadeSettings | null,
  section: K,
): boolean {
  if (!a || !b) return false
  return JSON.stringify(a[section]) !== JSON.stringify(b[section])
}

/** True when any section in draft differs from saved. */
function computeIsDirty(saved: CascadeSettings | null, draft: CascadeSettings | null): boolean {
  if (!saved || !draft) return false
  return JSON.stringify(saved) !== JSON.stringify(draft)
}

export function useSettings(): UseSettingsResult {
  const [settings, setSettings] = useState<CascadeSettings | null>(null)
  const [draft, setDraft] = useState<CascadeSettings | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Keep a stable ref to current saved settings for callbacks.
  const savedRef = useRef<CascadeSettings | null>(null)

  const load = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const data = await invoke<CascadeSettings>('get_settings')
      setSettings(data)
      setDraft(data)
      savedRef.current = data
    } catch {
      // Fall back to defaults in browser/Vitest
      const def = defaultSettings()
      setSettings(def)
      setDraft(def)
      savedRef.current = def
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const updateSection = useCallback(
    <K extends keyof CascadeSettings>(section: K, value: CascadeSettings[K]) => {
      setDraft((prev) => (prev ? { ...prev, [section]: value } : prev))
    },
    [],
  )

  const saveSection = useCallback(
    async <K extends keyof CascadeSettings>(section: K) => {
      setError(null)
      if (!draft) return

      // Only send the mutated section.
      const partial: SettingsUpdate = { [section]: draft[section] }
      try {
        await invoke('update_settings', { partial })
        // Merge saved state so isDirty resets for this section.
        setSettings((prev) => (prev ? { ...prev, [section]: draft[section] } : prev))
        savedRef.current = draft
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err)
        setError(msg)
      }
    },
    [draft],
  )

  const discard = useCallback(() => {
    setDraft(savedRef.current)
    setError(null)
  }, [])

  const isDirty = computeIsDirty(settings, draft)

  return {
    settings,
    draft,
    isLoading,
    isDirty,
    updateSection,
    saveSection,
    discard,
    reload: load,
    error,
  }
}

// ── Helpers exported for testing ──────────────────────────────────────────────

/** Returns true when the named section in draft differs from saved. */
export function isSectionDirty<K extends keyof CascadeSettings>(
  saved: CascadeSettings | null,
  draft: CascadeSettings | null,
  section: K,
): boolean {
  return sectionChanged(saved, draft, section)
}
