/**
 * Purpose: Wizard Phase 6 — content verification quality gate.
 *   Left panel reads back the written CASCADE.md content (via cascade_resolve).
 *   Right panel shows a per-tool, per-kind checklist derived from scan + merge data.
 *   User confirms everything important was captured before archiving.
 *
 * Inputs:
 *   - WizardContext via useWizard():
 *       state.detectedToolIds — tools found at scan time
 *       state.mergeResults    — per-tier AI merge results (sourceFile attributions)
 *       state.mergeResult     — legacy summary merge result (for empty-state gate)
 *       goNext, goBack, markComplete
 *
 * Outputs:
 *   - Left panel: cascadeContent (markdown rendered via <pre>) loaded via cascade_resolve
 *   - Right panel: VerifyChecklist (per-tool × kind checklist)
 *   - "Back to merge" button — goes to MergeContent step (WizardStep.MergeContent)
 *   - "Proceed to archive" button — active only when all checklist items resolved
 *   - Empty / no-merge state: shows a notice and allows proceeding without checklist
 *
 * Constraints:
 *   - cascade_resolve is a Tauri command stub until the daemon ships in P2.
 *     Error case (daemon not running, file not written yet) renders a graceful notice
 *     with the raw error message rather than crashing.
 *   - No editing of CASCADE.md content here (spec: out of scope for this ticket;
 *     inline editor is a per-item action wired to MergeContent phase via jumpTo).
 *   - Re-run merge per item calls mergeForTier via Tauri run_ai_merge (T-P3-E03-25).
 *     Since we don't have per-kind scoped mergeForTier in this ticket's scope, the
 *     re-run opens MergeContent step for the user to retrigger there.
 *   - markComplete(VerifyDiff) called on "Proceed" click.
 *
 * SPORT: MASTER-COMPONENTS.md — VerifyDiffPhase — wizard step 6 — ✅
 * Task: T-P3-E03-32
 */

import * as React from 'react'
import { invoke } from '@tauri-apps/api/core'
import { AlertCircle, ArrowLeft, CheckCircle2, ChevronRight, FileText, Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { ToolId, FileKind } from '@/lib/scanner/types'
import { useWizard } from '@/features/onboarding/WizardContext'
import { WizardStep } from '@/features/onboarding/types'
import { VerifyChecklist } from './VerifyChecklist'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type LoadStatus = 'idle' | 'loading' | 'loaded' | 'error'

// ---------------------------------------------------------------------------
// Inline alert — matches sibling phases (until E-01 shadcn wired)
// ---------------------------------------------------------------------------

function Alert({
  children,
  className,
  variant = 'default',
}: {
  children: React.ReactNode
  className?: string
  variant?: 'default' | 'destructive' | 'warning'
}) {
  return (
    <div
      role="alert"
      className={cn(
        'rounded-md border p-4',
        variant === 'destructive' && 'border-destructive/50 text-destructive',
        variant === 'warning' && 'border-yellow-500/50 text-yellow-700 dark:text-yellow-400',
        className
      )}
    >
      {children}
    </div>
  )
}

// ---------------------------------------------------------------------------
// VerifyDiffPhase
// ---------------------------------------------------------------------------

/**
 * Phase 6 of the onboarding wizard — verification quality gate.
 *
 * Reads back the cascade content written in Phase 4 (via cascade_resolve) and
 * presents a checklist confirming that each legacy tool's content was captured.
 * The user must resolve all checklist items (accept or skip) before proceeding
 * to the Archive phase.
 *
 * Empty state: if mergeResults is empty and detectedToolIds is null/empty,
 * the checklist is skipped and the user can proceed immediately with a note.
 *
 * @example
 * ```tsx
 * // Wired via WizardRouter case WizardStep.VerifyDiff
 * case WizardStep.VerifyDiff:
 *   return <VerifyDiffPhase />
 * ```
 */
export function VerifyDiffPhase() {
  const { state, goNext, jumpTo, markComplete } = useWizard()
  const { detectedToolIds, mergeResults, mergeResult } = state

  // ---------------------------------------------------------------------------
  // CASCADE.md content — loaded via cascade_resolve
  // ---------------------------------------------------------------------------

  const [cascadeContent, setCascadeContent] = React.useState<string>('')
  const [loadStatus, setLoadStatus] = React.useState<LoadStatus>('idle')
  const [loadError, setLoadError] = React.useState<string>('')

  React.useEffect(() => {
    let cancelled = false
    setLoadStatus('loading')

    invoke<{ content: string; format: string; tier: string }>('cascade_resolve', {
      tier: 'full',
      format: 'markdown',
    })
      .then((result) => {
        if (!cancelled) {
          setCascadeContent(result.content)
          setLoadStatus('loaded')
        }
      })
      .catch((err) => {
        if (!cancelled) {
          const msg = err instanceof Error ? err.message : String(err)
          setLoadError(msg)
          setLoadStatus('error')
        }
      })

    return () => {
      cancelled = true
    }
  }, [])

  // ---------------------------------------------------------------------------
  // Checklist state — tracks when all items are resolved
  // ---------------------------------------------------------------------------

  const [allResolved, setAllResolved] = React.useState(false)

  const handleAllResolved = React.useCallback(() => setAllResolved(true), [])

  // ---------------------------------------------------------------------------
  // Empty / no-merge state detection
  // ---------------------------------------------------------------------------

  const hasMergeData = mergeResult !== null || Object.keys(mergeResults).length > 0

  const hasDetectedTools = detectedToolIds !== null && detectedToolIds.length > 0

  // If nothing was scanned or merged, the checklist is irrelevant — skip it.
  const emptyState = !hasMergeData && !hasDetectedTools

  // Proceed is enabled when: empty state (skip), or all checklist items resolved.
  const canProceed = emptyState || allResolved

  // ---------------------------------------------------------------------------
  // Re-run merge: open MergeContent step for the user to retrigger
  // (per-kind scoped re-run is out of scope; see spec § out_of_scope)
  // ---------------------------------------------------------------------------

  const handleRerunMerge = React.useCallback(
    (_toolId: ToolId, _kind: FileKind) => {
      jumpTo(WizardStep.MergeContent)
    },
    [jumpTo]
  )

  // ---------------------------------------------------------------------------
  // Proceed
  // ---------------------------------------------------------------------------

  const handleProceed = React.useCallback(() => {
    markComplete(WizardStep.VerifyDiff)
    goNext()
  }, [markComplete, goNext])

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className="flex h-full flex-col gap-0">
      {/* Header */}
      <div className="border-b px-8 py-6">
        <h2 className="flex items-center gap-2 text-2xl font-semibold text-foreground">
          <CheckCircle2 className="h-6 w-6 text-primary" aria-hidden="true" />
          Verify Cascade Content
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Confirm that all important content from your legacy tool configs was captured before
          archiving.
        </p>
      </div>

      {/* Body — two-panel layout */}
      <div className="flex flex-1 overflow-hidden">
        {/* Left panel — CASCADE.md content */}
        <section
          aria-label="Your Cascade Content"
          className="flex w-1/2 flex-col gap-4 overflow-y-auto border-r px-6 py-5"
        >
          <h3 className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
            <FileText className="h-4 w-4" aria-hidden="true" />
            Your Cascade Content
          </h3>

          {/* Loading state */}
          {loadStatus === 'loading' && (
            <div
              role="status"
              aria-live="polite"
              className="flex items-center gap-2 py-4 text-sm text-muted-foreground"
            >
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              Loading cascade content…
            </div>
          )}

          {/* Error state */}
          {loadStatus === 'error' && (
            <Alert variant="warning">
              <div className="flex items-start gap-2">
                <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
                <div>
                  <p className="font-medium">Could not read CASCADE.md</p>
                  <p className="mt-1 text-xs opacity-80">{loadError}</p>
                  <p className="mt-2 text-xs">
                    This is expected if the cascade daemon is not yet running (Phase 2 ships the
                    daemon). You can still proceed — the checklist below reflects what was merged.
                  </p>
                </div>
              </div>
            </Alert>
          )}

          {/* Loaded content */}
          {loadStatus === 'loaded' && cascadeContent && (
            <pre className="whitespace-pre-wrap rounded-md bg-muted/40 p-4 text-xs text-foreground font-mono leading-relaxed overflow-x-auto">
              {cascadeContent}
            </pre>
          )}

          {/* No content written yet */}
          {loadStatus === 'loaded' && !cascadeContent && (
            <Alert>
              <p className="text-sm text-muted-foreground">
                No CASCADE.md content found. The file may not have been written yet. Return to the
                Merge step to generate and approve content.
              </p>
            </Alert>
          )}
        </section>

        {/* Right panel — checklist */}
        <section
          aria-label="Legacy Sources checklist"
          className="flex w-1/2 flex-col gap-4 overflow-y-auto px-6 py-5"
        >
          <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
            Legacy Sources
          </h3>

          {emptyState ? (
            <Alert variant="default">
              <div className="flex items-start gap-2">
                <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-green-500" />
                <div>
                  <p className="font-medium text-foreground">No legacy content to verify</p>
                  <p className="mt-1 text-sm text-muted-foreground">
                    No tools were scanned and no content was merged. You can proceed directly to the
                    Archive step.
                  </p>
                </div>
              </div>
            </Alert>
          ) : (
            <VerifyChecklist
              detectedToolIds={detectedToolIds}
              mergeResults={mergeResults}
              onRerunMerge={handleRerunMerge}
              onAllResolved={handleAllResolved}
            />
          )}
        </section>
      </div>

      {/* Footer actions */}
      <div className="flex items-center justify-between border-t px-8 py-4">
        {/* Back to merge */}
        <button
          type="button"
          onClick={() => jumpTo(WizardStep.MergeContent)}
          className="flex items-center gap-1 rounded px-3 py-2 text-sm text-muted-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ArrowLeft className="h-4 w-4" />
          Back to merge
        </button>

        {/* Proceed to archive */}
        <button
          type="button"
          onClick={handleProceed}
          disabled={!canProceed}
          className={cn(
            'flex items-center gap-1 rounded-md px-4 py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
            canProceed
              ? 'bg-primary text-primary-foreground hover:bg-primary/90'
              : 'cursor-not-allowed bg-muted text-muted-foreground'
          )}
        >
          Proceed to archive
          <ChevronRight className="h-4 w-4" />
        </button>
      </div>
    </div>
  )
}
