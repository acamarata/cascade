/**
 * Purpose: Per-tool confirmation dialog shown before archiving a legacy tool home.
 *
 * Inputs:
 *   - open: whether the dialog is visible
 *   - toolId: kebab-case identifier of the tool to be archived
 *   - originalRoot: absolute path of the directory being moved
 *   - archiveRoot: absolute resolved destination inside ~/.cascade/legacy/
 *   - fileCount: number of files that will be moved (for display)
 *   - onConfirm: called when user confirms the archive
 *   - onCancel: called when user cancels
 *
 * Outputs: Modal AlertDialog with exact source and destination paths shown.
 *
 * Constraints:
 *   - Never auto-confirms on mount — must be triggered by explicit user click.
 *   - Uses AlertDialog destructive variant (shadcn inline-primitive fallback until E-01).
 *   - All path display uses trimHome() for readability.
 *   - Messaging is non-destructive: emphasises files are MOVED and restorable.
 *
 * SPORT: MASTER-COMPONENTS.md — ArchiveConfirmDialog — wizard step 7
 * Tasks: T-P3-E03-20
 */

import { useCallback, useEffect, useRef } from 'react'
import { Archive } from 'lucide-react'
import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// Path helper — trims HOME prefix for readability
// ---------------------------------------------------------------------------

function trimHome(path: string): string {
  if (path.startsWith('/Users/') || path.startsWith('/home/')) {
    const parts = path.split('/')
    return '~/' + parts.slice(3).join('/')
  }
  return path
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface ArchiveConfirmDialogProps {
  open: boolean
  toolId: string
  /** Absolute original root directory being moved. */
  originalRoot: string
  /** Absolute destination root inside ~/.cascade/legacy/. */
  archiveRoot: string
  /** Number of files that will be moved. */
  fileCount: number
  onConfirm: () => void
  onCancel: () => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * Modal confirmation dialog shown before archiving a single legacy tool.
 *
 * Displays the exact source and destination paths so the user knows what
 * will be moved. Uses non-destructive messaging: files are moved and can
 * be restored at any time.
 */
export function ArchiveConfirmDialog({
  open,
  toolId,
  originalRoot,
  archiveRoot,
  fileCount,
  onConfirm,
  onCancel,
}: ArchiveConfirmDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const previousFocusRef = useRef<HTMLElement | null>(null)

  // Focus management: move into dialog on open, restore on close
  useEffect(() => {
    if (open) {
      previousFocusRef.current = document.activeElement as HTMLElement
      const raf = requestAnimationFrame(() => {
        const firstFocusable = dialogRef.current?.querySelector<HTMLElement>(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
        )
        ;(firstFocusable ?? dialogRef.current)?.focus()
      })
      return () => cancelAnimationFrame(raf)
    } else {
      previousFocusRef.current?.focus()
    }
  }, [open])

  // Focus trap + Esc closes
  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLDivElement>) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onCancel()
        return
      }
      if (e.key !== 'Tab') return
      const focusableSelectors =
        'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
      const focusable = Array.from(
        dialogRef.current?.querySelectorAll<HTMLElement>(focusableSelectors) ?? [],
      )
      if (focusable.length === 0) return
      const first = focusable[0]!
      const last = focusable[focusable.length - 1]!
      if (e.shiftKey) {
        if (document.activeElement === first) {
          e.preventDefault()
          last.focus()
        }
      } else {
        if (document.activeElement === last) {
          e.preventDefault()
          first.focus()
        }
      }
    },
    [onCancel],
  )

  if (!open) return null

  const displayTool = toolId
    .split('-')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')

  return (
    <>
      {/* Backdrop */}
      <div
        className="fixed inset-0 z-40 bg-black/50"
        aria-hidden="true"
        onClick={onCancel}
      />

      {/* Dialog */}
      <div
        ref={dialogRef}
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="archive-confirm-title"
        aria-describedby="archive-confirm-desc"
        tabIndex={-1}
        onKeyDown={handleKeyDown}
        className="fixed left-1/2 top-1/2 z-50 w-full max-w-md -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-background p-6 shadow-xl focus:outline-none"
      >
        {/* Icon + title */}
        <div className="mb-4 flex items-center gap-3">
          <div className="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-full bg-amber-100 dark:bg-amber-900/30">
            <Archive className="h-5 w-5 text-amber-600 dark:text-amber-400" aria-hidden="true" />
          </div>
          <h2
            id="archive-confirm-title"
            className="text-base font-semibold text-foreground"
          >
            Archive {displayTool}?
          </h2>
        </div>

        {/* Body */}
        <div id="archive-confirm-desc" className="space-y-3 text-sm text-muted-foreground">
          <p>
            <span className="font-medium text-foreground">{fileCount}</span>{' '}
            {fileCount === 1 ? 'file' : 'files'} will be <strong>moved</strong> (not
            deleted) to a safe backup location. You can restore them at any time.
          </p>

          {/* Path summary */}
          <div className="rounded-md border border-border bg-muted/40 p-3 text-xs font-mono space-y-2">
            <div>
              <span className="text-muted-foreground">From: </span>
              <span className="text-foreground break-all">{trimHome(originalRoot)}</span>
            </div>
            <div>
              <span className="text-muted-foreground">To:   </span>
              <span className="text-foreground break-all">{trimHome(archiveRoot)}</span>
            </div>
          </div>

          <p className="text-xs text-muted-foreground">
            Cascade will not touch your{' '}
            <span className="font-mono text-foreground">~/.cascade/</span> directory.
          </p>
        </div>

        {/* Actions */}
        <div className={cn('mt-6 flex items-center justify-end gap-3')}>
          <button
            type="button"
            onClick={onCancel}
            className="rounded-md border border-border px-4 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={onConfirm}
            className="rounded-md bg-amber-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-amber-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:bg-amber-500 dark:hover:bg-amber-600"
          >
            Move to archive
          </button>
        </div>
      </div>
    </>
  )
}
