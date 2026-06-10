/**
 * Purpose: Inline confirmation card for deleting a GCI hook by hook_id.
 * Inputs: hookId ("{event}:{index}") — path param for DELETE /api/gci/hooks-write/:hook_id.
 *         event   — displayed in the confirmation message.
 *         onSuccess() — called on 200/204; caller removes row and shows toast.
 *         onError(msg) — called on non-ok response; caller shows error toast.
 *         onCancel()   — called when user clicks "No, keep it".
 * Outputs: DELETE /api/gci/hooks-write/:hook_id on confirm.
 * Constraints: No auth header required (endpoint unauthenticated, same pattern as GET).
 *   Inline card — no modal/portal dependency.
 * SPORT: T-P3-E02-27 DeleteConfirm
 */
import { useState } from 'react'
import { Button } from '@/components/ui/button'

interface DeleteConfirmProps {
  hookId: string
  event: string
  onSuccess: () => void
  onError: (message: string) => void
  onCancel: () => void
}

export function DeleteConfirm({ hookId, event, onSuccess, onError, onCancel }: DeleteConfirmProps) {
  const [deleting, setDeleting] = useState(false)

  async function handleConfirm() {
    setDeleting(true)
    try {
      const res = await fetch(`/api/gci/hooks-write/${encodeURIComponent(hookId)}`, {
        method: 'DELETE',
      })

      if (res.status === 404) {
        onError('Hook not found — it may have already been deleted.')
        return
      }

      if (!res.ok) {
        const json = (await res.json().catch(() => ({}))) as { error?: string }
        onError(json.error ?? `API error ${res.status}`)
        return
      }

      onSuccess()
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Network error.')
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div className="rounded border border-destructive/40 bg-destructive/5 px-3 py-3 flex flex-col gap-2 text-sm">
      <p className="text-sm">
        Remove hook for <span className="font-mono font-semibold">{event}</span>?
      </p>
      <div className="flex gap-2 justify-end">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onCancel}
          disabled={deleting}
        >
          No, keep it
        </Button>
        <Button
          type="button"
          variant="destructive"
          size="sm"
          onClick={handleConfirm}
          disabled={deleting}
        >
          {deleting ? 'Removing…' : 'Yes, remove'}
        </Button>
      </div>
    </div>
  )
}
