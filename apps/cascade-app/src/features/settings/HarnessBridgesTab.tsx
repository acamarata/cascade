/**
 * Purpose: Settings tab for harness bridge config-file paths.
 *   Each harness (CC, OC, Codex) has a path Input, a Browse button (Tauri dialog.open),
 *   and a Detect button (invokes detect_harness_path IPC).
 * Inputs:  draft HarnessBridgesSettings; saveSection callback.
 * Outputs: <section> with three path rows; saves via update_settings { harnessBridges }.
 * Constraints:
 *   - Tauri file picker via @tauri-apps/plugin-dialog (dialog.open).
 *   - detect_harness_path IPC returns the default path for each harness slug.
 *   - Falls back gracefully when Tauri is not available (browser/Vitest).
 *   - WCAG 2.1 AA: all inputs labelled; focus ring on all interactive elements.
 * SPORT: MASTER-COMPONENTS.md — HarnessBridgesTab — T-P3-E07-15
 */

import { useState } from 'react'
import { FolderOpen, Crosshair } from 'lucide-react'
import { invoke } from '@tauri-apps/api/core'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { HarnessBridgesSettings } from '@/types/settings'

// ── Tauri file picker (guard for browser env) ─────────────────────────────────

async function pickFile(): Promise<string | null> {
  try {
    const { open } = await import('@tauri-apps/plugin-dialog')
    const result = await open({ directory: false, multiple: false })
    if (Array.isArray(result)) return result[0] ?? null
    return result
  } catch {
    return null
  }
}

// ── Single harness row ────────────────────────────────────────────────────────

interface HarnessRowProps {
  id: string
  label: string
  slug: string
  value: string
  onChange: (v: string) => void
}

function HarnessRow({ id, label, slug, value, onChange }: HarnessRowProps) {
  const [detecting, setDetecting] = useState(false)

  async function handleBrowse() {
    const path = await pickFile()
    if (path) onChange(path)
  }

  async function handleDetect() {
    setDetecting(true)
    try {
      const path = await invoke<string>('detect_harness_path', { harness: slug })
      if (path) onChange(path)
    } catch {
      // IPC not yet wired — silently no-op
    } finally {
      setDetecting(false)
    }
  }

  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-sm font-medium">
        {label}
      </label>
      <div className="flex gap-2">
        <Input
          id={id}
          type="text"
          value={value}
          placeholder={`Path to ${slug} config…`}
          spellCheck={false}
          onChange={(e) => onChange(e.target.value)}
          className="flex-1 font-mono text-xs"
        />
        <button
          type="button"
          aria-label={`Browse for ${label}`}
          onClick={handleBrowse}
          className="flex items-center justify-center h-9 px-3 gap-1.5 rounded-md border border-input bg-transparent text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          <FolderOpen className="h-4 w-4" aria-hidden="true" />
          Browse
        </button>
        <button
          type="button"
          aria-label={`Auto-detect ${label}`}
          disabled={detecting}
          onClick={handleDetect}
          className="flex items-center justify-center h-9 px-3 gap-1.5 rounded-md border border-input bg-transparent text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:opacity-50"
        >
          <Crosshair className="h-4 w-4" aria-hidden="true" />
          {detecting ? 'Detecting…' : 'Detect'}
        </button>
      </div>
    </div>
  )
}

// ── HarnessBridgesTab ─────────────────────────────────────────────────────────

export interface HarnessBridgesTabProps {
  draft: HarnessBridgesSettings
  isSaving: boolean
  error: string | null
  onUpdate: (val: HarnessBridgesSettings) => void
  onSave: () => void
  onDiscard: () => void
}

export function HarnessBridgesTab({
  draft,
  isSaving,
  error,
  onUpdate,
  onSave,
  onDiscard,
}: HarnessBridgesTabProps) {
  function set<K extends keyof HarnessBridgesSettings>(key: K, val: string) {
    onUpdate({ ...draft, [key]: val || null })
  }

  return (
    <section aria-labelledby="harness-tab-heading" className="space-y-6 max-w-xl">
      <h2 id="harness-tab-heading" className="text-base font-semibold">
        Harness Bridges
      </h2>
      <p className="text-sm text-muted-foreground">
        Point each harness at its config file. Use Detect to auto-fill the standard location.
      </p>

      <HarnessRow
        id="cc-config-path"
        label="Claude Code (CC) config path"
        slug="cc"
        value={draft.ccConfigPath ?? ''}
        onChange={(v) => set('ccConfigPath', v)}
      />

      <HarnessRow
        id="oc-config-path"
        label="OpenCode (OC) config path"
        slug="oc"
        value={draft.ocConfigPath ?? ''}
        onChange={(v) => set('ocConfigPath', v)}
      />

      <HarnessRow
        id="codex-config-path"
        label="Codex config path"
        slug="codex"
        value={draft.codexConfigPath ?? ''}
        onChange={(v) => set('codexConfigPath', v)}
      />

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex gap-2 pt-2">
        <Button type="button" onClick={onSave} disabled={isSaving}>
          {isSaving ? 'Saving…' : 'Save Bridges'}
        </Button>
        <Button type="button" variant="outline" onClick={onDiscard} disabled={isSaving}>
          Discard
        </Button>
      </div>
    </section>
  )
}
