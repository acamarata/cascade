/**
 * Purpose: Renders the list of GCI hooks fetched from GET /api/gci/hooks.
 *          Owns all fetch/refresh state; delegates edit/delete callbacks to children.
 * Inputs: none (fetches independently); exposes onAddClick, setEditing, setDeleting callbacks internally.
 * Outputs: Card with hook rows (event, command, label) and Add button.
 *          Shows empty state, loading spinner, error with retry.
 * Constraints: Pure Tailwind, no shadcn Table. Refresh on demand via refetch exposed to HookForm/DeleteConfirm.
 * SPORT: T-P3-E02-27 HooksList
 */
import { useState } from 'react'
import { useGCI } from '@/hooks/useGCI'
import type { HookEntry, HooksResponse } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { HookForm } from './HookForm'
import { DeleteConfirm } from './DeleteConfirm'
import { cn } from '@/lib/utils'

/** A hook entry augmented with its list index, used to compute hook_id for DELETE. */
interface IndexedHook {
  hook: HookEntry
  index: number
}

export function HooksList() {
  const { data, loading, error, refetch } = useGCI<HooksResponse>('/api/gci/hooks')
  const hooks = data?.hooks ?? []

  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<IndexedHook | null>(null)
  const [deleting, setDeleting] = useState<IndexedHook | null>(null)

  // Toast state: { message, variant }
  const [toast, setToast] = useState<{ message: string; variant: 'success' | 'error' } | null>(null)

  function showToast(message: string, variant: 'success' | 'error') {
    setToast({ message, variant })
    setTimeout(() => setToast(null), 3500)
  }

  function handleAddClick() {
    setEditing(null)
    setShowForm(true)
  }

  function handleEditClick(hook: HookEntry, index: number) {
    setEditing({ hook, index })
    setShowForm(true)
  }

  function handleFormSuccess(msg: string) {
    setShowForm(false)
    setEditing(null)
    showToast(msg, 'success')
    refetch()
  }

  function handleFormError(msg: string) {
    showToast(msg, 'error')
  }

  function handleDeleteSuccess() {
    setDeleting(null)
    showToast('Hook removed.', 'success')
    refetch()
  }

  function handleDeleteError(msg: string) {
    setDeleting(null)
    showToast(msg, 'error')
  }

  return (
    <div className="rounded-lg border border-border bg-card text-card-foreground p-4 flex flex-col gap-3">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h2 className="font-semibold text-base">Hooks Editor</h2>
        <Button size="sm" onClick={handleAddClick} disabled={showForm}>
          + Add Hook
        </Button>
      </div>

      {/* Inline toast */}
      {toast && (
        <div
          className={cn(
            'rounded px-3 py-2 text-sm',
            toast.variant === 'success'
              ? 'bg-green-50 text-green-800 dark:bg-green-900/30 dark:text-green-300'
              : 'bg-red-50 text-red-800 dark:bg-red-900/30 dark:text-red-300',
          )}
        >
          {toast.message}
        </div>
      )}

      {/* Add / Edit form */}
      {showForm && (
        <HookForm
          initial={editing?.hook ?? null}
          hookId={editing !== null ? `${editing.hook.event}:${editing.index}` : null}
          onSuccess={handleFormSuccess}
          onError={handleFormError}
          onCancel={() => { setShowForm(false); setEditing(null) }}
        />
      )}

      {/* Loading */}
      {loading && (
        <div className="flex items-center justify-center h-16 text-muted-foreground text-sm">
          <span className="animate-spin mr-2">⟳</span> Loading…
        </div>
      )}

      {/* Error */}
      {error && !loading && (
        <div className="flex flex-col items-center gap-2 text-destructive text-sm py-4">
          <span>{error}</span>
          <button
            onClick={refetch}
            className="text-xs underline text-muted-foreground hover:text-foreground"
          >
            Retry
          </button>
        </div>
      )}

      {/* Empty state */}
      {!loading && !error && hooks.length === 0 && !showForm && (
        <p className="text-sm text-muted-foreground py-4 text-center">
          No hooks configured yet. Add one to get started.
        </p>
      )}

      {/* Hook rows */}
      {!loading && !error && hooks.length > 0 && (
        <div className="flex flex-col gap-1">
          {hooks.map((hook, idx) => (
            <div
              key={`${hook.event}:${idx}`}
              className="flex items-start justify-between gap-2 rounded border border-border/50 bg-muted/20 px-3 py-2 text-sm hover:bg-muted/40 transition-colors"
            >
              <div className="flex flex-col gap-0.5 min-w-0">
                <span className="font-mono text-xs font-semibold text-primary">{hook.event}</span>
                <code className="font-mono text-xs text-foreground/80 truncate max-w-[28rem]" title={hook.command}>
                  {hook.command}
                </code>
                {hook.matcher && (
                  <span className="text-xs text-muted-foreground">matcher: {hook.matcher}</span>
                )}
              </div>
              <div className="flex gap-1 shrink-0">
                <Button
                  size="sm"
                  variant="outline"
                  className="text-xs h-7 px-2"
                  onClick={() => handleEditClick(hook, idx)}
                >
                  Edit
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  className="text-xs h-7 px-2"
                  onClick={() => setDeleting({ hook, index: idx })}
                >
                  Delete
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Delete confirmation */}
      {deleting && (
        <DeleteConfirm
          hookId={`${deleting.hook.event}:${deleting.index}`}
          event={deleting.hook.event}
          onSuccess={handleDeleteSuccess}
          onError={handleDeleteError}
          onCancel={() => setDeleting(null)}
        />
      )}
    </div>
  )
}
