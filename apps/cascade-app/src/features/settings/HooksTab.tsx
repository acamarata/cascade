/**
 * Purpose: Settings tab for lifecycle hook definitions.
 *   DataTable with event/command/enabled columns; add row (Select for event); remove row.
 * Inputs:  draft HookDefinition[]; saveSection callback.
 * Outputs: <section> table with add/remove; saves hooks[] via update_settings partial.
 * Constraints:
 *   - Hook execution is handled by P2/E-02 engine — this is UI only.
 *   - Event is a constrained Select (five lifecycle event names).
 *   - IDs generated client-side (crypto.randomUUID or fallback).
 *   - WCAG 2.1 AA: table has correct role, all inputs labelled.
 * SPORT: MASTER-COMPONENTS.md — HooksTab — T-P3-E07-16
 */

import { Plus, Trash2 } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { HookDefinition } from '@/types/settings'

// ── Constants ─────────────────────────────────────────────────────────────────

export const HOOK_EVENTS = [
  'SessionStart',
  'UserPromptSubmit',
  'PreToolUse',
  'PostToolUse',
  'Stop',
] as const

export type HookEvent = (typeof HOOK_EVENTS)[number]

function nextId(): string {
  return typeof crypto !== 'undefined' && crypto.randomUUID
    ? crypto.randomUUID()
    : `hook-${Date.now()}`
}

// ── HooksTab ──────────────────────────────────────────────────────────────────

export interface HooksTabProps {
  draft: HookDefinition[]
  isSaving: boolean
  error: string | null
  onUpdate: (val: HookDefinition[]) => void
  onSave: () => void
  onDiscard: () => void
}

export function HooksTab({
  draft,
  isSaving,
  error,
  onUpdate,
  onSave,
  onDiscard,
}: HooksTabProps) {
  function addRow() {
    onUpdate([
      ...draft,
      { id: nextId(), event: 'SessionStart', command: '', enabled: true },
    ])
  }

  function removeRow(idx: number) {
    onUpdate(draft.filter((_, i) => i !== idx))
  }

  function updateRow<K extends keyof HookDefinition>(idx: number, key: K, val: HookDefinition[K]) {
    const next = [...draft]
    next[idx] = { ...next[idx], [key]: val }
    onUpdate(next)
  }

  return (
    <section aria-labelledby="hooks-tab-heading" className="space-y-4">
      <h2 id="hooks-tab-heading" className="text-base font-semibold">
        Hooks
      </h2>
      <p className="text-sm text-muted-foreground">
        Define shell commands to run on lifecycle events. Hook execution is managed by the daemon.
      </p>

      <div className="rounded-md border border-border overflow-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border bg-muted/50">
              <th scope="col" className="px-3 py-2 text-left font-medium w-44">
                Event
              </th>
              <th scope="col" className="px-3 py-2 text-left font-medium">
                Command
              </th>
              <th scope="col" className="px-3 py-2 text-center font-medium w-20">
                Enabled
              </th>
              <th scope="col" className="px-3 py-2 w-10">
                <span className="sr-only">Remove</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {draft.length === 0 && (
              <tr>
                <td colSpan={4} className="px-3 py-4 text-center text-muted-foreground text-sm">
                  No hooks configured. Click Add to create one.
                </td>
              </tr>
            )}
            {draft.map((hook, idx) => (
              <tr key={hook.id} className="border-b border-border last:border-0">
                <td className="px-3 py-2">
                  <select
                    aria-label={`Event for hook ${idx + 1}`}
                    value={hook.event}
                    onChange={(e) => updateRow(idx, 'event', e.target.value)}
                    className="w-full rounded-md border border-input bg-transparent px-2 py-1 text-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  >
                    {HOOK_EVENTS.map((ev) => (
                      <option key={ev} value={ev}>
                        {ev}
                      </option>
                    ))}
                  </select>
                </td>
                <td className="px-3 py-2">
                  <Input
                    type="text"
                    value={hook.command}
                    placeholder="echo hello"
                    aria-label={`Command for hook ${idx + 1}`}
                    onChange={(e) => updateRow(idx, 'command', e.target.value)}
                    className="text-xs font-mono"
                  />
                </td>
                <td className="px-3 py-2 text-center">
                  <button
                    type="button"
                    role="switch"
                    aria-checked={hook.enabled}
                    aria-label={`${hook.enabled ? 'Disable' : 'Enable'} hook ${idx + 1}`}
                    onClick={() => updateRow(idx, 'enabled', !hook.enabled)}
                    className={[
                      'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors',
                      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
                      hook.enabled ? 'bg-primary' : 'bg-input',
                    ].join(' ')}
                  >
                    <span
                      className={[
                        'pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform',
                        hook.enabled ? 'translate-x-4' : 'translate-x-0',
                      ].join(' ')}
                    />
                    <span className="sr-only">{hook.enabled ? 'Enabled' : 'Disabled'}</span>
                  </button>
                </td>
                <td className="px-3 py-2">
                  <button
                    type="button"
                    aria-label={`Remove hook ${idx + 1}`}
                    onClick={() => removeRow(idx)}
                    className="flex items-center justify-center h-7 w-7 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  >
                    <Trash2 className="h-4 w-4" aria-hidden="true" />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <Button type="button" variant="outline" size="sm" onClick={addRow}>
        <Plus className="h-4 w-4 mr-1.5" aria-hidden="true" />
        Add hook
      </Button>

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex gap-2 pt-2">
        <Button type="button" onClick={onSave} disabled={isSaving}>
          {isSaving ? 'Saving…' : 'Save Hooks'}
        </Button>
        <Button type="button" variant="outline" onClick={onDiscard} disabled={isSaving}>
          Discard
        </Button>
      </div>
    </section>
  )
}
