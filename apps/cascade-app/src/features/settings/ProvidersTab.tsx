/**
 * Purpose: Settings tab for per-provider API key configuration.
 *   Renders a masked password input with toggle-show button per provider.
 *   Saves per-provider via update_settings partial (providers section only).
 * Inputs:  draft ProvidersSettings from useSettings; saveSection callback.
 * Outputs: <section> with Anthropic / OpenAI / Google (multi-key) / Codex inputs.
 * Constraints:
 *   - Keys NEVER logged to console (type=password; no console.log in callbacks).
 *   - Google field manages an array of keys (add/remove rows).
 *   - Partial update: saving Providers does not touch GeminiPool or other sections.
 *   - WCAG 2.1 AA: all inputs labelled; toggle uses aria-pressed.
 * SPORT: MASTER-COMPONENTS.md — ProvidersTab — T-P3-E07-15
 */

import { useState } from 'react'
import { Eye, EyeOff, Plus, Trash2 } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { ProvidersSettings } from '@/types/settings'

// ── Masked key input ──────────────────────────────────────────────────────────

interface MaskedKeyInputProps {
  id: string
  label: string
  value: string
  placeholder?: string
  onChange: (val: string) => void
}

function MaskedKeyInput({ id, label, value, placeholder, onChange }: MaskedKeyInputProps) {
  const [visible, setVisible] = useState(false)
  const toggleId = `${id}-toggle`

  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-sm font-medium">
        {label}
      </label>
      <div className="flex gap-2">
        <Input
          id={id}
          type={visible ? 'text' : 'password'}
          value={value}
          placeholder={placeholder ?? 'Enter API key…'}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => onChange(e.target.value)}
          className="flex-1 font-mono text-xs"
        />
        <button
          id={toggleId}
          type="button"
          aria-pressed={visible}
          aria-label={visible ? `Hide ${label}` : `Show ${label}`}
          onClick={() => setVisible((v) => !v)}
          className="flex items-center justify-center h-9 w-9 rounded-md border border-input bg-transparent hover:bg-accent focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          {visible ? (
            <EyeOff className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          ) : (
            <Eye className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          )}
        </button>
      </div>
    </div>
  )
}

// ── Google key list ───────────────────────────────────────────────────────────

interface GoogleKeysProps {
  keys: string[]
  onChange: (keys: string[]) => void
}

interface GoogleKeyRowProps {
  idx: number
  value: string
  onUpdate: (idx: number, val: string) => void
  onRemove: (idx: number) => void
}

/** Single Google API key row — isolated component so useState is at component scope. */
function GoogleKeyRow({ idx, value, onUpdate, onRemove }: GoogleKeyRowProps) {
  const [vis, setVis] = useState(false)
  const inputId = `google-key-${idx}`
  return (
    <div className="flex gap-2">
      <Input
        id={inputId}
        type={vis ? 'text' : 'password'}
        value={value}
        placeholder={`Google key ${idx + 1}…`}
        autoComplete="off"
        spellCheck={false}
        onChange={(e) => onUpdate(idx, e.target.value)}
        aria-label={`Google API key ${idx + 1}`}
        className="flex-1 font-mono text-xs"
      />
      <button
        type="button"
        aria-pressed={vis}
        aria-label={`${vis ? 'Hide' : 'Show'} Google key ${idx + 1}`}
        onClick={() => setVis((v) => !v)}
        className="flex items-center justify-center h-9 w-9 rounded-md border border-input bg-transparent hover:bg-accent focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
      >
        {vis ? (
          <EyeOff className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        ) : (
          <Eye className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        )}
      </button>
      <button
        type="button"
        aria-label={`Remove Google key ${idx + 1}`}
        onClick={() => onRemove(idx)}
        className="flex items-center justify-center h-9 w-9 rounded-md border border-destructive/40 bg-transparent hover:bg-destructive/10 text-destructive focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
      >
        <Trash2 className="h-4 w-4" aria-hidden="true" />
      </button>
    </div>
  )
}

function GoogleKeyList({ keys, onChange }: GoogleKeysProps) {
  function update(idx: number, val: string) {
    const next = [...keys]
    next[idx] = val
    onChange(next)
  }

  function addRow() {
    onChange([...keys, ''])
  }

  function removeRow(idx: number) {
    onChange(keys.filter((_, i) => i !== idx))
  }

  return (
    <div className="space-y-1.5">
      <span className="block text-sm font-medium">Google API Keys</span>
      {keys.map((k, idx) => (
        <GoogleKeyRow key={idx} idx={idx} value={k} onUpdate={update} onRemove={removeRow} />
      ))}
      <Button type="button" variant="outline" size="sm" onClick={addRow} className="mt-1">
        <Plus className="h-4 w-4 mr-1.5" aria-hidden="true" />
        Add Google key
      </Button>
    </div>
  )
}

// ── ProvidersTab ──────────────────────────────────────────────────────────────

export interface ProvidersTabProps {
  draft: ProvidersSettings
  isSaving: boolean
  error: string | null
  onUpdate: (val: ProvidersSettings) => void
  onSave: () => void
  onDiscard: () => void
}

export function ProvidersTab({
  draft,
  isSaving,
  error,
  onUpdate,
  onSave,
  onDiscard,
}: ProvidersTabProps) {
  function set<K extends keyof ProvidersSettings>(key: K, val: ProvidersSettings[K]) {
    onUpdate({ ...draft, [key]: val })
  }

  return (
    <section aria-labelledby="providers-tab-heading" className="space-y-6 max-w-xl">
      <h2 id="providers-tab-heading" className="text-base font-semibold">
        AI Providers
      </h2>

      <MaskedKeyInput
        id="anthropic-key"
        label="Anthropic API Key"
        value={draft.anthropic ?? ''}
        placeholder="sk-ant-…"
        onChange={(v) => set('anthropic', v || null)}
      />

      <MaskedKeyInput
        id="openai-key"
        label="OpenAI API Key"
        value={draft.openai ?? ''}
        placeholder="sk-…"
        onChange={(v) => set('openai', v || null)}
      />

      <GoogleKeyList keys={draft.google} onChange={(v) => set('google', v)} />

      <MaskedKeyInput
        id="codex-key"
        label="Codex API Key"
        value={draft.codex ?? ''}
        placeholder="sk-codex-…"
        onChange={(v) => set('codex', v || null)}
      />

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex gap-2 pt-2">
        <Button type="button" onClick={onSave} disabled={isSaving}>
          {isSaving ? 'Saving…' : 'Save Providers'}
        </Button>
        <Button type="button" variant="outline" onClick={onDiscard} disabled={isSaving}>
          Discard
        </Button>
      </div>
    </section>
  )
}
