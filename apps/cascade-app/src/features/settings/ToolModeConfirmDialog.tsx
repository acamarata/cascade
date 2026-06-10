/**
 * Purpose: Confirmation dialog for per-tool Cascade-managed ↔ Independent mode flip.
 *   Shows which paths will be added (link) or removed (unlink) before any FS mutation.
 *
 * Inputs:
 *   - toolLabel: Human-readable tool name (e.g. "Claude Code").
 *   - direction: "manage" (Independent → Cascade-managed) or "unmanage" (Cascade-managed → Independent).
 *   - predictedPaths: File paths that will be added/removed (shown in the preview).
 *   - onConfirm: () => void — called when user clicks confirm; caller drives the IPC.
 *   - onCancel: () => void — called when user dismisses without confirming.
 *
 * Outputs: Modal dialog with confirm/cancel controls and a path preview list.
 *
 * Constraints:
 *   - No IPC side-effects — caller is responsible for the actual IPC call.
 *   - WCAG 2.1 AA: focus-visible rings, role="dialog", aria-modal, aria-labelledby.
 *   - Direction "unmanage" uses destructive button style (red) to signal removal.
 *   - Direction "manage" uses the default primary button style.
 *
 * SPORT: MASTER-COMPONENTS.md — ToolModeConfirmDialog — settings/ — T-P3-E03-37
 * Task: T-P3-E03-37
 */

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { X, Link2, Link2Off } from 'lucide-react'

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export type FlipDirection = 'manage' | 'unmanage'

export interface ToolModeConfirmDialogProps {
  toolLabel: string
  direction: FlipDirection
  /** Paths that will be created (manage) or removed (unmanage). May be empty when unknown. */
  predictedPaths?: string[]
  onConfirm: () => void
  onCancel: () => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * Modal dialog confirming a per-tool mode flip before any filesystem mutation.
 *
 * Rendered outside the row table to avoid stacking-context issues.
 */
export function ToolModeConfirmDialog({
  toolLabel,
  direction,
  predictedPaths = [],
  onConfirm,
  onCancel,
}: ToolModeConfirmDialogProps) {
  const isManage = direction === 'manage'
  const Icon = isManage ? Link2 : Link2Off

  return (
    /* Backdrop */
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40"
      role="dialog"
      aria-modal="true"
      aria-labelledby="mode-dialog-title"
    >
      <div className="relative w-full max-w-md rounded-lg border border-border bg-background p-6 shadow-xl">
        {/* Close button */}
        <button
          type="button"
          onClick={onCancel}
          className={cn(
            'absolute right-4 top-4 rounded p-1 text-muted-foreground',
            'hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
          )}
          aria-label="Cancel"
        >
          <X className="h-4 w-4" aria-hidden="true" />
        </button>

        <div className="space-y-4">
          {/* Title */}
          <div className="flex items-center gap-2 pr-8">
            <Icon
              className={cn(
                'h-5 w-5 flex-shrink-0',
                isManage ? 'text-primary' : 'text-destructive'
              )}
              aria-hidden="true"
            />
            <h2 id="mode-dialog-title" className="text-base font-semibold">
              {isManage
                ? `Make ${toolLabel} Cascade-managed?`
                : `Restore ${toolLabel} to Independent?`}
            </h2>
          </div>

          {/* Explanation */}
          <p className="text-sm text-muted-foreground">
            {isManage ? (
              <>
                Cascade will create sibling symlinks so{' '}
                <span className="font-medium text-foreground">{toolLabel}</span> reads your
                CASCADE.md instructions at each tier (GCI, APC, PPC, …).
              </>
            ) : (
              <>
                The sibling symlinks for{' '}
                <span className="font-medium text-foreground">{toolLabel}</span> will be removed.
                Real files are never touched — only symlinks pointing to CASCADE.md are removed.
              </>
            )}
          </p>

          {/* Path preview */}
          {predictedPaths.length > 0 && (
            <div className="rounded-md border border-border bg-muted/30 px-3 py-2 space-y-1">
              <p className="text-xs font-medium text-muted-foreground">
                Paths that will be {isManage ? 'created' : 'removed'}:
              </p>
              <ul className="space-y-0.5">
                {predictedPaths.map((p) => (
                  <li key={p} className="text-xs font-mono text-foreground truncate">
                    {p}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {predictedPaths.length === 0 && (
            <p className="text-xs text-muted-foreground italic">
              Exact paths will be determined at runtime.
            </p>
          )}

          {/* Actions */}
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="outline" size="sm" onClick={onCancel}>
              Cancel
            </Button>
            <Button size="sm" variant={isManage ? 'default' : 'destructive'} onClick={onConfirm}>
              {isManage ? 'Make Cascade-managed' : 'Restore Independence'}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
