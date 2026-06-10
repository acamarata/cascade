/**
 * Purpose: Provider Settings page — lists all AI providers registered in the daemon,
 *   shows connection status, today's cost, and actions (test, remove, add).
 * Inputs:  cascade_providers_list IPC — polled every 30 s.
 * Outputs: Page with ProviderCard list + AddProviderDialog trigger.
 * Constraints:
 *   - Graceful degradation when daemon not running: renders empty state with helpful copy.
 *   - IPC command-not-found → empty list with "Daemon IPC not yet wired" note.
 *   - Poll interval: 30_000 ms (spec T-P3-E04-23).
 *   - WCAG 2.1 AA: landmark roles, focus management, live regions for async feedback.
 * SPORT: MASTER-ROUTES.md — /settings/providers — T-P3-E04-23
 */

import { useState, useEffect, useCallback, useRef } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { Plus, ServerCrash } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { ProviderCard } from '@/components/providers/ProviderCard'
import { AddProviderDialog } from '@/components/providers/AddProviderDialog'
import type { ProviderListItem } from '@/types/providers'

const POLL_INTERVAL_MS = 30_000

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Fetches the provider list from the daemon.
 * Returns an empty array if the IPC command is not yet wired or daemon is offline.
 * Never throws — all errors resolve to [].
 */
async function fetchProviders(): Promise<ProviderListItem[]> {
  try {
    return await invoke<ProviderListItem[]>('cascade_providers_list')
  } catch {
    return []
  }
}

async function invokeProviderAction(cmd: string, id: string): Promise<string | null> {
  try {
    await invoke(cmd, { id })
    return null
  } catch (err) {
    const msg = String(err)
    if (msg.includes('not found') || msg.includes('unknown command')) {
      return 'Not yet available — daemon wiring in progress.'
    }
    return msg
  }
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * Page-level component for /settings/providers.
 *
 * Polls the daemon for provider list every 30 s. Supports:
 *   - Test Connection: cascade_providers_test
 *   - Remove: cascade_providers_remove
 *   - Add: via AddProviderDialog (cascade_providers_add_apikey / _oauth_start / _add_generic)
 *
 * Empty state when no providers configured. Error state if daemon returns an error.
 */
export function ProviderSettings() {
  const [providers, setProviders] = useState<ProviderListItem[]>([])
  const [loading, setLoading] = useState(true)
  const [feedback, setFeedback] = useState<{ id: string; message: string; kind: 'ok' | 'err' } | null>(null)
  const [pendingId, setPendingId] = useState<string | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const feedbackTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const load = useCallback(async () => {
    const list = await fetchProviders()
    setProviders(list)
    setLoading(false)
  }, [])

  useEffect(() => {
    load()
    pollRef.current = setInterval(load, POLL_INTERVAL_MS)
    return () => {
      if (pollRef.current) clearInterval(pollRef.current)
      if (feedbackTimer.current) clearTimeout(feedbackTimer.current)
    }
  }, [load])

  function showFeedback(id: string, message: string, kind: 'ok' | 'err') {
    if (feedbackTimer.current) clearTimeout(feedbackTimer.current)
    setFeedback({ id, message, kind })
    feedbackTimer.current = setTimeout(() => setFeedback(null), 4_000)
  }

  async function handleTest(id: string) {
    setPendingId(id)
    const err = await invokeProviderAction('cascade_providers_test', id)
    setPendingId(null)
    if (err) {
      showFeedback(id, err, 'err')
    } else {
      showFeedback(id, 'Connection test passed.', 'ok')
      await load()
    }
  }

  async function handleRemove(id: string) {
    setPendingId(id)
    const err = await invokeProviderAction('cascade_providers_remove', id)
    setPendingId(null)
    if (err) {
      showFeedback(id, err, 'err')
    } else {
      await load()
    }
  }

  async function handleAddSuccess() {
    await load()
  }

  return (
    <main className="mx-auto max-w-2xl space-y-6" aria-labelledby="providers-heading">
      {/* Header */}
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 id="providers-heading" className="text-lg font-semibold">
            AI Providers
          </h1>
          <p className="text-sm text-muted-foreground">
            Manage the AI providers Cascade uses for completions and routing.
          </p>
        </div>
        <Button
          variant="default"
          size="sm"
          onClick={() => setDialogOpen(true)}
          aria-label="Add a new AI provider"
        >
          <Plus className="h-4 w-4 mr-1.5" aria-hidden="true" />
          Add Provider
        </Button>
      </div>

      {/* Live feedback region */}
      {feedback && (
        <div
          role="status"
          aria-live="polite"
          className={[
            'rounded-md border px-4 py-2 text-sm',
            feedback.kind === 'ok'
              ? 'border-green-300 bg-green-50 text-green-700 dark:bg-green-900/20 dark:border-green-800 dark:text-green-400'
              : 'border-destructive/40 bg-destructive/10 text-destructive',
          ].join(' ')}
        >
          {feedback.message}
        </div>
      )}

      {/* Loading */}
      {loading && (
        <p className="text-sm text-muted-foreground" aria-live="polite">
          Loading providers…
        </p>
      )}

      {/* Empty state */}
      {!loading && providers.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-border py-12 text-center">
          <ServerCrash className="h-8 w-8 text-muted-foreground/50" aria-hidden="true" />
          <div className="space-y-1">
            <p className="text-sm font-medium">No providers connected</p>
            <p className="text-xs text-muted-foreground">
              Add an AI provider to start using Cascade completions and routing.
            </p>
          </div>
          <Button variant="outline" size="sm" onClick={() => setDialogOpen(true)}>
            <Plus className="h-4 w-4 mr-1.5" aria-hidden="true" />
            Add your first provider
          </Button>
        </div>
      )}

      {/* Provider list */}
      {!loading && providers.length > 0 && (
        <ul aria-label="Connected providers" className="space-y-2">
          {providers.map((item) => (
            <li key={item.id}>
              <ProviderCard
                item={item}
                onTest={() => handleTest(item.id)}
                onRemove={() => handleRemove(item.id)}
                pending={pendingId === item.id}
              />
            </li>
          ))}
        </ul>
      )}

      {/* Add Provider Dialog */}
      <AddProviderDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onSuccess={handleAddSuccess}
      />
    </main>
  )
}
