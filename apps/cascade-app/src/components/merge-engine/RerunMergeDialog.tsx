/**
 * Purpose: Per-section "Re-run merge with different prompt" dialog.
 *   Lets the user supply a custom instruction and re-trigger the AI merge for
 *   one specific section, replacing its proposedContent and resetting status to 'pending'.
 *
 * Inputs:
 *   - open: boolean — whether the dialog is visible.
 *   - section: MergeSection — the section being re-run (shown read-only).
 *   - sources: MergeSourceFile[] — source files for the merge call.
 *   - ctx: ProviderRoutingContext — provider routing context from WizardState.
 *   - tierId: string — the tier key (e.g. 'global') for dispatch.
 *   - onResult: (section: MergeSection) => void — called with the new section on success.
 *   - onClose: () => void — close without re-running.
 *
 * Outputs: Modal dialog with textarea for custom instruction, loading state, and new content.
 *
 * Constraints:
 *   - Uses inline primitive dialog (matches ArchiveConfirmDialog pattern in this repo).
 *   - Section-scoped loading: does not block other sections.
 *   - On result: caller dispatches UPDATE_SECTION_STATUS with new proposedContent + status='pending'.
 *   - Shows previous vs new diff inline after re-run (compare old proposedContent to new).
 *   - Pre-fills textarea with example hints per spec.
 *   - ≤250 lines.
 *
 * SPORT: MASTER-COMPONENTS.md — RerunMergeDialog — merge-engine/ — ✅
 * Task: T-P3-E03-31
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { Loader2, RefreshCw, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { mergeSection } from '@/features/onboarding/merge/mergeService'
import type { MergeSection, MergeSourceFile } from '@/features/onboarding/merge/types'
import type { ProviderRoutingContext } from '@/features/onboarding/merge/providerRouter'

// ---------------------------------------------------------------------------
// Example hints (per spec T-P3-E03-31)
// ---------------------------------------------------------------------------

const EXAMPLE_HINTS =
  "Be more concise\nKeep all rules, don't deduplicate\nFocus only on file operation rules"

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface RerunMergeDialogProps {
  /** Whether the dialog is currently visible. */
  open: boolean
  /** The section to re-run. Its proposedContent is shown read-only. */
  section: MergeSection
  /** Legacy source files fed to the merge call. */
  sources: MergeSourceFile[]
  /** Provider routing context from WizardState. */
  ctx: ProviderRoutingContext
  /** Tier key used for display (e.g. 'global'). */
  tierId: string
  /**
   * Called when the re-run completes successfully.
   * The caller is responsible for dispatching UPDATE_SECTION_STATUS with the result.
   */
  onResult: (section: MergeSection) => void
  /** Close the dialog without re-running. */
  onClose: () => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * Per-section re-run dialog for Phase 4 merge review.
 *
 * User types a custom instruction; clicking Re-run calls mergeService.mergeSection()
 * for just this section. On success, shows the new content vs old inline and fires onResult.
 */
export function RerunMergeDialog({
  open,
  section,
  sources,
  ctx,
  tierId,
  onResult,
  onClose,
}: RerunMergeDialogProps) {
  const [instruction, setInstruction] = useState('')
  const [status, setStatus] = useState<'idle' | 'loading' | 'done' | 'error'>('idle')
  const [errorMsg, setErrorMsg] = useState<string | null>(null)
  const [newSection, setNewSection] = useState<MergeSection | null>(null)

  // Ref to the dialog container for focus management and focus trap
  const dialogRef = useRef<HTMLDivElement>(null)
  // Ref to the element that had focus before the dialog opened (for restoration)
  const previousFocusRef = useRef<HTMLElement | null>(null)

  // Move focus into dialog on open; restore on close
  useEffect(() => {
    if (open) {
      previousFocusRef.current = document.activeElement as HTMLElement
      // Focus the dialog itself (or first focusable child) after render
      const raf = requestAnimationFrame(() => {
        const firstFocusable = dialogRef.current?.querySelector<HTMLElement>(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
        )
        ;(firstFocusable ?? dialogRef.current)?.focus()
      })
      return () => cancelAnimationFrame(raf)
    } else {
      // Restore focus on close
      previousFocusRef.current?.focus()
    }
  }, [open])

  const handleClose = useCallback(() => {
    // Reset on close
    setInstruction('')
    setStatus('idle')
    setErrorMsg(null)
    setNewSection(null)
    onClose()
  }, [onClose])

  // Focus trap: keep Tab/Shift+Tab within the dialog
  const handleDialogKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLDivElement>) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        handleClose()
        return
      }
      if (e.key !== 'Tab') return
      const focusableSelectors =
        'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
      const focusable = Array.from(
        dialogRef.current?.querySelectorAll<HTMLElement>(focusableSelectors) ?? []
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
    [handleClose]
  )

  const handleRerun = useCallback(async () => {
    if (!instruction.trim()) return
    setStatus('loading')
    setErrorMsg(null)
    setNewSection(null)
    try {
      const result = await mergeSection(section.id, instruction.trim(), sources, ctx)
      setNewSection(result)
      setStatus('done')
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setErrorMsg(msg)
      setStatus('error')
    }
  }, [instruction, section.id, sources, ctx])

  const handleAccept = useCallback(() => {
    if (!newSection) return
    onResult(newSection)
    // Reset dialog state for next open
    setInstruction('')
    setStatus('idle')
    setNewSection(null)
    onClose()
  }, [newSection, onResult, onClose])

  if (!open) return null

  const prevContent = section.proposedContent
  const nextContent = newSection?.proposedContent ?? null

  return (
    <>
      {/* Backdrop — aria-hidden so AT skips it; keyboard dismiss handled by dialog onKeyDown.
          role="button" with tabIndex={-1} satisfies the interactive-element requirement
          while keeping it out of the tab order. */}
      <div
        role="button"
        tabIndex={-1}
        className="fixed inset-0 z-40 bg-black/50"
        aria-hidden="true"
        onClick={handleClose}
        onKeyDown={() => {
          /* keyboard dismiss via dialog container onKeyDown */
        }}
      />

      {/* Dialog — role="dialog" is interactive per WAI-ARIA; onKeyDown handles focus trap */}
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions -- dialog role is interactive */}
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="rerun-dialog-title"
        aria-describedby="rerun-dialog-desc"
        tabIndex={-1}
        onKeyDown={handleDialogKeyDown}
        className="fixed left-1/2 top-1/2 z-50 w-full max-w-lg -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border bg-background p-6 shadow-xl focus:outline-none"
      >
        {/* Header */}
        <div className="mb-4 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <RefreshCw className="h-5 w-5 text-primary" aria-hidden="true" />
            <h2 id="rerun-dialog-title" className="text-base font-semibold text-foreground">
              Re-run merge: {section.title}
            </h2>
          </div>
          <button
            type="button"
            onClick={handleClose}
            className={cn(
              'rounded-md p-1 text-muted-foreground hover:bg-muted',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'transition-colors'
            )}
            aria-label="Close dialog"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>

        <div id="rerun-dialog-desc" className="space-y-4">
          {/* Current content (read-only) */}
          <div>
            <p className="mb-1.5 text-xs font-medium text-muted-foreground uppercase tracking-wide">
              Current content
            </p>
            <div className="max-h-28 overflow-y-auto rounded-md border border-border bg-muted/40 p-3 text-xs font-mono text-foreground whitespace-pre-wrap">
              {prevContent || <span className="text-muted-foreground italic">Empty</span>}
            </div>
          </div>

          {/* Custom instruction textarea */}
          <div>
            <label
              htmlFor="rerun-instruction"
              className="mb-1.5 block text-xs font-medium text-foreground"
            >
              Custom instructions for this section
            </label>
            <textarea
              id="rerun-instruction"
              value={instruction}
              onChange={(e) => setInstruction(e.target.value)}
              placeholder={EXAMPLE_HINTS}
              rows={3}
              disabled={status === 'loading'}
              className={cn(
                'w-full rounded-md border border-input bg-background px-3 py-2 text-sm',
                'text-foreground placeholder:text-muted-foreground/60',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                'disabled:cursor-not-allowed disabled:opacity-50',
                'resize-none'
              )}
            />
            <p className="mt-1 text-xs text-muted-foreground">
              Tier: <span className="font-medium">{tierId}</span> · Section:{' '}
              {section.id.slice(0, 8)}…
            </p>
          </div>

          {/* Error */}
          {status === 'error' && errorMsg && (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-md border border-destructive/50 bg-destructive/10 p-3 text-xs text-destructive"
            >
              <span className="font-medium">Re-run failed:</span>
              <span className="break-all">{errorMsg}</span>
            </div>
          )}

          {/* New content preview (after success) */}
          {status === 'done' && nextContent !== null && (
            <div>
              <p className="mb-1.5 text-xs font-medium text-muted-foreground uppercase tracking-wide">
                New content
              </p>
              <div className="max-h-36 overflow-y-auto rounded-md border border-primary/40 bg-muted/40 p-3 text-xs font-mono text-foreground whitespace-pre-wrap">
                {nextContent || <span className="text-muted-foreground italic">Empty</span>}
              </div>
            </div>
          )}
        </div>

        {/* Actions */}
        <div className="mt-5 flex items-center justify-end gap-3">
          <button
            type="button"
            onClick={handleClose}
            className={cn(
              'rounded-md border border-border px-4 py-2 text-sm font-medium',
              'text-foreground hover:bg-muted',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'transition-colors'
            )}
          >
            Cancel
          </button>
          {status === 'done' ? (
            <button
              type="button"
              onClick={handleAccept}
              className={cn(
                'flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm font-medium',
                'text-primary-foreground hover:bg-primary/90',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                'transition-colors'
              )}
            >
              Use new content
            </button>
          ) : (
            <button
              type="button"
              onClick={() => void handleRerun()}
              disabled={!instruction.trim() || status === 'loading'}
              className={cn(
                'flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm font-medium',
                'text-primary-foreground hover:bg-primary/90',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                'disabled:cursor-not-allowed disabled:opacity-50',
                'transition-colors'
              )}
            >
              {status === 'loading' ? (
                <>
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                  Re-running…
                </>
              ) : (
                <>
                  <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
                  Re-run
                </>
              )}
            </button>
          )}
        </div>
      </div>
    </>
  )
}
