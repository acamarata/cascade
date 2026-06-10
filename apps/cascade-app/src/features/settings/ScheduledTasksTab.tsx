/**
 * Purpose: Settings tab for scheduled task definitions.
 *   DataTable with name/schedule/command/enabled columns; add/remove rows.
 *   Cron expression validated on blur (5 or 6 field; red border if invalid).
 * Inputs:  draft ScheduledTaskEntry[]; saveSection callback.
 * Outputs: <section> table; saves scheduledTasks[] via update_settings partial.
 * Constraints:
 *   - Cron validation is UI-only (regex); the daemon validates on execution.
 *   - IDs generated client-side.
 *   - WCAG 2.1 AA: table roles, inputs labelled, invalid state communicated via aria-invalid.
 * SPORT: MASTER-COMPONENTS.md — ScheduledTasksTab — T-P3-E07-16
 */

import { useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { ScheduledTaskEntry } from '@/types/settings'

// ── Cron validation ───────────────────────────────────────────────────────────

/**
 * Validates a single cron field token.
 * Accepts: * | *\/n | n | n-n | n,n | n-n,n | compound with step (e.g. 1-5\/2)
 */
const CRON_FIELD = /^(\*(?:\/\d+)?|(?:\d+(-\d+)?)(?:(?:,\d+(?:-\d+)?)*)?(?:\/\d+)?)$/

/** Returns true for valid 5-field or 6-field cron expressions. */
export function isValidCron(expr: string): boolean {
  const parts = expr.trim().split(/\s+/)
  if (parts.length !== 5 && parts.length !== 6) return false
  return parts.every((p) => CRON_FIELD.test(p))
}

function nextId(): string {
  return typeof crypto !== 'undefined' && crypto.randomUUID
    ? crypto.randomUUID()
    : `task-${Date.now()}`
}

// ── ScheduledTasksTab ─────────────────────────────────────────────────────────

export interface ScheduledTasksTabProps {
  draft: ScheduledTaskEntry[]
  isSaving: boolean
  error: string | null
  onUpdate: (val: ScheduledTaskEntry[]) => void
  onSave: () => void
  onDiscard: () => void
}

export function ScheduledTasksTab({
  draft,
  isSaving,
  error,
  onUpdate,
  onSave,
  onDiscard,
}: ScheduledTasksTabProps) {
  // Track which cron fields have been touched (for validation display).
  const [cronErrors, setCronErrors] = useState<Record<number, boolean>>({})

  function addRow() {
    onUpdate([
      ...draft,
      { id: nextId(), label: '', cron: '0 2 * * *', command: '', enabled: true },
    ])
  }

  function removeRow(idx: number) {
    onUpdate(draft.filter((_, i) => i !== idx))
    setCronErrors((prev) => {
      const next = { ...prev }
      delete next[idx]
      return next
    })
  }

  function updateRow<K extends keyof ScheduledTaskEntry>(
    idx: number,
    key: K,
    val: ScheduledTaskEntry[K],
  ) {
    const next = [...draft]
    next[idx] = { ...next[idx], [key]: val }
    onUpdate(next)
  }

  function validateCron(idx: number, val: string) {
    setCronErrors((prev) => ({ ...prev, [idx]: !isValidCron(val) }))
  }

  const hasCronError = Object.values(cronErrors).some(Boolean)

  return (
    <section aria-labelledby="scheduled-tasks-tab-heading" className="space-y-4">
      <h2 id="scheduled-tasks-tab-heading" className="text-base font-semibold">
        Scheduled Tasks
      </h2>
      <p className="text-sm text-muted-foreground">
        Tasks are executed by the daemon on the given cron schedule.
      </p>

      <div className="rounded-md border border-border overflow-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border bg-muted/50">
              <th scope="col" className="px-3 py-2 text-left font-medium w-36">
                Label
              </th>
              <th scope="col" className="px-3 py-2 text-left font-medium w-40">
                Cron
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
                <td colSpan={5} className="px-3 py-4 text-center text-muted-foreground text-sm">
                  No scheduled tasks. Click Add to create one.
                </td>
              </tr>
            )}
            {draft.map((task, idx) => {
              const cronInvalid = !!cronErrors[idx]
              return (
                <tr key={task.id} className="border-b border-border last:border-0">
                  <td className="px-3 py-2">
                    <Input
                      type="text"
                      value={task.label}
                      placeholder="Task name"
                      aria-label={`Label for task ${idx + 1}`}
                      onChange={(e) => updateRow(idx, 'label', e.target.value)}
                      className="text-xs"
                    />
                  </td>
                  <td className="px-3 py-2">
                    <Input
                      type="text"
                      value={task.cron}
                      placeholder="0 2 * * *"
                      aria-label={`Cron schedule for task ${idx + 1}`}
                      aria-invalid={cronInvalid}
                      aria-describedby={cronInvalid ? `cron-error-${idx}` : undefined}
                      onChange={(e) => updateRow(idx, 'cron', e.target.value)}
                      onBlur={(e) => validateCron(idx, e.target.value)}
                      className={[
                        'text-xs font-mono',
                        cronInvalid ? 'border-destructive focus-visible:ring-destructive' : '',
                      ].join(' ')}
                    />
                    {cronInvalid && (
                      <p
                        id={`cron-error-${idx}`}
                        role="alert"
                        className="mt-0.5 text-xs text-destructive"
                      >
                        Invalid cron expression
                      </p>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <Input
                      type="text"
                      value={task.command}
                      placeholder="Command or prompt"
                      aria-label={`Command for task ${idx + 1}`}
                      onChange={(e) => updateRow(idx, 'command', e.target.value)}
                      className="text-xs font-mono"
                    />
                  </td>
                  <td className="px-3 py-2 text-center">
                    <button
                      type="button"
                      role="switch"
                      aria-checked={task.enabled}
                      aria-label={`${task.enabled ? 'Disable' : 'Enable'} task ${idx + 1}`}
                      onClick={() => updateRow(idx, 'enabled', !task.enabled)}
                      className={[
                        'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors',
                        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
                        task.enabled ? 'bg-primary' : 'bg-input',
                      ].join(' ')}
                    >
                      <span
                        className={[
                          'pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform',
                          task.enabled ? 'translate-x-4' : 'translate-x-0',
                        ].join(' ')}
                      />
                      <span className="sr-only">{task.enabled ? 'Enabled' : 'Disabled'}</span>
                    </button>
                  </td>
                  <td className="px-3 py-2">
                    <button
                      type="button"
                      aria-label={`Remove task ${idx + 1}`}
                      onClick={() => removeRow(idx)}
                      className="flex items-center justify-center h-7 w-7 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                    >
                      <Trash2 className="h-4 w-4" aria-hidden="true" />
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      <Button type="button" variant="outline" size="sm" onClick={addRow}>
        <Plus className="h-4 w-4 mr-1.5" aria-hidden="true" />
        Add task
      </Button>

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex gap-2 pt-2">
        <Button type="button" onClick={onSave} disabled={isSaving || hasCronError}>
          {isSaving ? 'Saving…' : 'Save Tasks'}
        </Button>
        <Button type="button" variant="outline" onClick={onDiscard} disabled={isSaving}>
          Discard
        </Button>
      </div>
    </section>
  )
}
