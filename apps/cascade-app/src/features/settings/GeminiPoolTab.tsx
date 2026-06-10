/**
 * Purpose: Settings tab for the Gemini proxy pool — key rotation list, port, enabled toggle.
 * Inputs:  draft GeminiPoolSettings; saveSection callback.
 * Outputs: <section> with key list (add/remove rows), port number input, enabled switch.
 * Constraints:
 *   - Keys are masked (type=password with toggle-show per row).
 *   - Port must be a valid TCP port (1–65535); validated on change.
 *   - Saving sends only the geminiPool section (partial update).
 *   - WCAG 2.1 AA: all inputs labelled; toggle button uses aria-pressed.
 * SPORT: MASTER-COMPONENTS.md — GeminiPoolTab — T-P3-E07-15
 */

import { useState } from 'react'
import { Eye, EyeOff, Plus, Trash2 } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { GeminiPoolSettings } from '@/types/settings'

// ── Key row ───────────────────────────────────────────────────────────────────

interface KeyRowProps {
  idx: number
  value: string
  onChange: (v: string) => void
  onRemove: () => void
}

function KeyRow({ idx, value, onChange, onRemove }: KeyRowProps) {
  const [visible, setVisible] = useState(false)
  const inputId = `gemini-key-${idx}`

  return (
    <div className="flex gap-2">
      <Input
        id={inputId}
        type={visible ? 'text' : 'password'}
        value={value}
        placeholder={`Gemini key ${idx + 1}…`}
        autoComplete="off"
        spellCheck={false}
        onChange={(e) => onChange(e.target.value)}
        aria-label={`Gemini pool key ${idx + 1}`}
        className="flex-1 font-mono text-xs"
      />
      <button
        type="button"
        aria-pressed={visible}
        aria-label={`${visible ? 'Hide' : 'Show'} Gemini key ${idx + 1}`}
        onClick={() => setVisible((v) => !v)}
        className="flex items-center justify-center h-9 w-9 rounded-md border border-input bg-transparent hover:bg-accent focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
      >
        {visible ? (
          <EyeOff className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        ) : (
          <Eye className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        )}
      </button>
      <button
        type="button"
        aria-label={`Remove Gemini key ${idx + 1}`}
        onClick={onRemove}
        className="flex items-center justify-center h-9 w-9 rounded-md border border-destructive/40 bg-transparent hover:bg-destructive/10 text-destructive focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
      >
        <Trash2 className="h-4 w-4" aria-hidden="true" />
      </button>
    </div>
  )
}

// ── GeminiPoolTab ─────────────────────────────────────────────────────────────

export interface GeminiPoolTabProps {
  draft: GeminiPoolSettings
  isSaving: boolean
  error: string | null
  onUpdate: (val: GeminiPoolSettings) => void
  onSave: () => void
  onDiscard: () => void
}

export function GeminiPoolTab({
  draft,
  isSaving,
  error,
  onUpdate,
  onSave,
  onDiscard,
}: GeminiPoolTabProps) {
  const [portError, setPortError] = useState<string | null>(null)

  function updateKey(idx: number, val: string) {
    const next = [...draft.keys]
    next[idx] = val
    onUpdate({ ...draft, keys: next })
  }

  function addKey() {
    onUpdate({ ...draft, keys: [...draft.keys, ''] })
  }

  function removeKey(idx: number) {
    onUpdate({ ...draft, keys: draft.keys.filter((_, i) => i !== idx) })
  }

  function updatePort(raw: string) {
    const n = parseInt(raw, 10)
    if (!raw || isNaN(n) || n < 1 || n > 65535) {
      setPortError('Port must be 1–65535')
    } else {
      setPortError(null)
      onUpdate({ ...draft, proxyPort: n })
    }
  }

  function toggleEnabled() {
    onUpdate({ ...draft, enabled: !draft.enabled })
  }

  return (
    <section aria-labelledby="gemini-pool-tab-heading" className="space-y-6 max-w-xl">
      <h2 id="gemini-pool-tab-heading" className="text-base font-semibold">
        Gemini Pool
      </h2>

      {/* Enabled toggle */}
      <div className="flex items-center gap-3">
        <label htmlFor="gemini-pool-enabled" className="text-sm font-medium">
          Enable Gemini proxy pool
        </label>
        <button
          id="gemini-pool-enabled"
          type="button"
          role="switch"
          aria-checked={draft.enabled}
          onClick={toggleEnabled}
          className={[
            'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
            draft.enabled ? 'bg-primary' : 'bg-input',
          ].join(' ')}
        >
          <span
            className={[
              'pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform',
              draft.enabled ? 'translate-x-4' : 'translate-x-0',
            ].join(' ')}
          />
          <span className="sr-only">{draft.enabled ? 'Enabled' : 'Disabled'}</span>
        </button>
      </div>

      {/* Proxy port */}
      <div className="space-y-1.5">
        <label htmlFor="gemini-proxy-port" className="block text-sm font-medium">
          Proxy port
        </label>
        <Input
          id="gemini-proxy-port"
          type="number"
          min={1}
          max={65535}
          defaultValue={draft.proxyPort}
          onBlur={(e) => updatePort(e.target.value)}
          aria-describedby={portError ? 'port-error' : undefined}
          aria-invalid={portError ? true : undefined}
          className="w-32"
        />
        {portError && (
          <p id="port-error" role="alert" className="text-xs text-destructive">
            {portError}
          </p>
        )}
      </div>

      {/* Key list */}
      <div className="space-y-2">
        <span className="block text-sm font-medium">API Keys in Rotation</span>
        {draft.keys.length === 0 && (
          <p className="text-sm text-muted-foreground">No keys configured.</p>
        )}
        {draft.keys.map((k, idx) => (
          <KeyRow
            key={idx}
            idx={idx}
            value={k}
            onChange={(v) => updateKey(idx, v)}
            onRemove={() => removeKey(idx)}
          />
        ))}
        <Button type="button" variant="outline" size="sm" onClick={addKey} className="mt-1">
          <Plus className="h-4 w-4 mr-1.5" aria-hidden="true" />
          Add key
        </Button>
      </div>

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex gap-2 pt-2">
        <Button type="button" onClick={onSave} disabled={isSaving || !!portError}>
          {isSaving ? 'Saving…' : 'Save Pool'}
        </Button>
        <Button type="button" variant="outline" onClick={onDiscard} disabled={isSaving}>
          Discard
        </Button>
      </div>
    </section>
  )
}
