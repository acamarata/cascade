/**
 * Purpose: Controlled form for adding or editing a GCI hook entry.
 * Inputs: initial (HookEntry | null) — pre-populates fields for edit mode.
 *         hookId (string | null) — present in edit mode; null = add mode.
 *         onSuccess(msg) — called after successful API response (caller refreshes list).
 *         onError(msg)   — called on non-validation API error (caller shows toast).
 *         onCancel       — called when user dismisses the form.
 * Outputs: POST /api/gci/hooks-write on submit; shows inline validation error on 400.
 * Constraints: No auth header required — endpoint is unauthenticated (same as GET /api/gci/hooks).
 *   Empty command rejected client-side before API call. 300-line cap.
 * SPORT: T-P3-E02-27 HookForm
 */
import { useState } from 'react'
import type { HookEntry } from '@/lib/api'
import { Button } from '@/components/ui/button'

/** Valid CC hook event names (allow-list per daemon spec). */
const VALID_EVENTS = [
  'SessionStart',
  'UserPromptSubmit',
  'PreToolUse',
  'PostToolUse',
  'Stop',
  'SubagentStop',
  'Notification',
] as const

type ValidEvent = (typeof VALID_EVENTS)[number]

interface HookFormProps {
  initial: HookEntry | null
  hookId: string | null
  onSuccess: (message: string) => void
  onError: (message: string) => void
  onCancel: () => void
}

export function HookForm({ initial, onSuccess, onError, onCancel }: HookFormProps) {
  const [event, setEvent] = useState<ValidEvent>(
    (initial?.event as ValidEvent) ?? 'UserPromptSubmit',
  )
  const [command, setCommand] = useState(initial?.command ?? '')
  const [matcher, setMatcher] = useState(initial?.matcher ?? '')
  const [cmdError, setCmdError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setCmdError(null)

    if (!command.trim()) {
      setCmdError('Command is required.')
      return
    }

    setSubmitting(true)
    try {
      const body: Record<string, string> = { event, command: command.trim() }
      if (matcher.trim()) body.matcher = matcher.trim()

      const res = await fetch('/api/gci/hooks-write', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })

      if (res.status === 400) {
        const json = (await res.json().catch(() => ({}))) as { error?: string }
        setCmdError(json.error ?? 'Validation error.')
        return
      }

      if (!res.ok) {
        const json = (await res.json().catch(() => ({}))) as { error?: string }
        onError(json.error ?? `API error ${res.status}`)
        return
      }

      onSuccess(initial ? 'Hook updated.' : 'Hook added.')
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Network error.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form
      onSubmit={handleSubmit}
      className="rounded border border-border bg-muted/30 p-3 flex flex-col gap-3 text-sm"
    >
      <p className="font-medium text-sm">{initial ? 'Edit Hook' : 'New Hook'}</p>

      {/* Event selector */}
      <div className="flex flex-col gap-1">
        <label className="text-xs text-muted-foreground font-medium" htmlFor="hook-event">
          Event
        </label>
        <select
          id="hook-event"
          value={event}
          onChange={e => setEvent(e.target.value as ValidEvent)}
          className="rounded border border-input bg-background px-2 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
          disabled={submitting}
        >
          {VALID_EVENTS.map(ev => (
            <option key={ev} value={ev}>
              {ev}
            </option>
          ))}
        </select>
      </div>

      {/* Command input */}
      <div className="flex flex-col gap-1">
        <label className="text-xs text-muted-foreground font-medium" htmlFor="hook-command">
          Command <span className="text-destructive">*</span>
        </label>
        <input
          id="hook-command"
          type="text"
          value={command}
          onChange={e => { setCommand(e.target.value); setCmdError(null) }}
          placeholder="e.g. echo 'hook fired'"
          className="rounded border border-input bg-background px-2 py-1.5 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-ring"
          disabled={submitting}
          autoComplete="off"
        />
        {cmdError && (
          <span className="text-xs text-destructive">{cmdError}</span>
        )}
      </div>

      {/* Matcher input (optional) */}
      <div className="flex flex-col gap-1">
        <label className="text-xs text-muted-foreground font-medium" htmlFor="hook-matcher">
          Matcher <span className="text-muted-foreground/70">(optional)</span>
        </label>
        <input
          id="hook-matcher"
          type="text"
          value={matcher}
          onChange={e => setMatcher(e.target.value)}
          placeholder="tool name pattern, e.g. Bash"
          className="rounded border border-input bg-background px-2 py-1.5 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-ring"
          disabled={submitting}
          autoComplete="off"
        />
      </div>

      {/* Actions */}
      <div className="flex gap-2 justify-end">
        <Button type="button" variant="outline" size="sm" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={submitting}>
          {submitting ? 'Saving…' : initial ? 'Update' : 'Add'}
        </Button>
      </div>
    </form>
  )
}
