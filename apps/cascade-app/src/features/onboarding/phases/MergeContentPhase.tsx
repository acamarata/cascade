/**
 * Purpose: Phase 4 — AI merge flow screen for the Cascade onboarding wizard.
 *   Triggers the AI merge, renders DiffPanel for review, and writes approved
 *   sections via write_cascade_content.
 *
 * Inputs:
 *   - WizardContext (useWizard): state.scanResult, state.detectedToolIds,
 *     state.mergeResults, state.providerConnected, state.selectedTemplates.
 *     Helpers: setMergeResult, approveSection, rejectSection, editSection,
 *     markComplete, setSelectedTemplates.
 *
 * Outputs:
 *   - (NEW T-P3-E05-19) Shows TemplatePickerCompact before the AI merge step.
 *     User picks templates; wizard stores selection in state.selectedTemplates.
 *   - Calls template_apply IPC for each selected template (tier first, then
 *     stack, then shape) before running the AI merge. Pre-scaffold result is
 *     passed as the starting point for the AI merge.
 *   - On template_apply error: shows inline error; user may skip and proceed bare.
 *   - Graceful when template IPC is absent (daemon not running): skips apply,
 *     proceeds to bare AI merge.
 *   - Calls mergeService.mergeForTier on mount (or shows cached result on resume).
 *   - Renders DiffPanel with MergeResult once merge completes.
 *   - Writes approved/edited sections via invoke('write_cascade_content') on Apply.
 *   - Calls markComplete(WizardStep.MergeContent) after successful write.
 *
 * Constraints:
 *   - Resume-safe: if mergeResults['global'] exists in context, skip picker + merge call.
 *   - "Apply to Cascade" disabled until all sections are reviewed (none pending).
 *   - Rejected sections excluded from write payload.
 *   - Edited sections use editedContent, not proposedContent.
 *   - Error state: show Merge failed + Retry + Skip option.
 *   - Parse error: show warning banner + allow proceeding.
 *   - No sources: show info + advance option.
 *
 * SPORT: MASTER-COMPONENTS.md — MergeContentPhase — wizard step 4 — ✅
 * Tasks: T-P3-E03-29, T-P3-E05-19
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { AlertTriangle, CheckCheck, GitMerge, Layers, RefreshCw, SkipForward, XCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useWizard } from '@/features/onboarding/WizardContext'
import { WizardStep } from '@/features/onboarding/types'
import { mergeForTier } from '@/features/onboarding/merge/mergeService'
import type { MergeSection, MergeSourceFile, MergeConflict } from '@/features/onboarding/merge/types'
import { DiffPanel } from '@/components/merge-engine/DiffPanel'
import { MergeLoadingState } from './MergeLoadingState'
import { TemplatePickerCompact } from '@/components/template/TemplatePickerCompact'
import type { TemplateEntryIpc } from '@/types/templates'
import type { ApplyResultIpc } from '@/types/templates'

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const TIER_KEY = 'global'

// ---------------------------------------------------------------------------
// Inline minimal alert (matches sibling phase pattern)
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
        'flex items-start gap-3 rounded-md border p-4 text-sm',
        variant === 'destructive' && 'border-destructive/50 bg-destructive/10 text-destructive',
        variant === 'warning' && 'border-amber-400/50 bg-amber-50/30 text-amber-800 dark:text-amber-200',
        variant === 'default' && 'border-border bg-muted/30 text-foreground',
        className,
      )}
    >
      {children}
    </div>
  )
}

// ---------------------------------------------------------------------------
// MergeContentPhase
// ---------------------------------------------------------------------------

interface MergeContentPhaseProps {
  /** Optional className forwarded to the outer container. */
  className?: string
}

/**
 * Phase 4 content panel: AI merge flow screen.
 *
 * Flow:
 * 1. On mount: check for cached mergeResults['global'] (resume path) — skip merge if present.
 * 2. Otherwise: call mergeForTier('global', sources) and show MergeLoadingState.
 * 3. On complete: render DiffPanel; user reviews/approves/rejects/edits sections.
 * 4. "Apply to Cascade" enabled when all sections reviewed (none pending).
 * 5. On Apply: write approved+edited sections via write_cascade_content, then markComplete.
 */
export function MergeContentPhase({ className }: MergeContentPhaseProps) {
  const {
    state,
    setMergeResult,
    setSelectedTemplates,
    approveSection,
    rejectSection,
    editSection,
    markComplete,
    getMergeResult,
  } = useWizard()

  // ---------------------------------------------------------------------------
  // Template picker state (T-P3-E05-19)
  // ---------------------------------------------------------------------------

  /** Catalog loaded from template_list IPC. Null = not yet fetched. */
  const [templateCatalog, setTemplateCatalog] = useState<TemplateEntryIpc[] | null>(null)
  const [templateCatalogError, setTemplateCatalogError] = useState<string | null>(null)
  const [templateCatalogLoading, setTemplateCatalogLoading] = useState(false)

  /** Error from template_apply (non-fatal — user can proceed bare). */
  const [templateApplyError, setTemplateApplyError] = useState<string | null>(null)

  /**
   * pickingTemplate: true when we show the picker before the AI merge.
   * Set to false once the user clicks "Proceed to AI merge" (or "Skip template").
   * Not shown on resume (cached result already exists).
   */
  const [pickingTemplate, setPickingTemplate] = useState(() => {
    // Resume: skip picker if merge already cached
    if (state.mergeResults[TIER_KEY]) return false
    // No detected tools: skip picker (no-sources state handles this)
    if (state.detectedToolIds !== null && state.detectedToolIds.length === 0) return false
    return true
  })

  // Load template catalog when picker is shown (lazy)
  useEffect(() => {
    if (!pickingTemplate || templateCatalog !== null || templateCatalogLoading) return
    setTemplateCatalogLoading(true)
    invoke<TemplateEntryIpc[]>('template_list', { filter: {} })
      .then((entries) => {
        setTemplateCatalog(entries)
        setTemplateCatalogError(null)
      })
      .catch((err: unknown) => {
        // Graceful: daemon may not be running yet; treat as empty catalog
        const msg = err instanceof Error ? err.message : String(err)
        setTemplateCatalog([])
        setTemplateCatalogError(msg)
      })
      .finally(() => {
        setTemplateCatalogLoading(false)
      })
  }, [pickingTemplate, templateCatalog, templateCatalogLoading])

  // ---------------------------------------------------------------------------
  // Merge status tracking
  // ---------------------------------------------------------------------------

  // Merge status tracking
  const [mergePhase, setMergePhase] = useState<'loading' | 'ready' | 'error' | 'writing' | 'no-sources'>(
    () => {
      // Resume: if cached result exists, go straight to ready
      const cached = state.mergeResults[TIER_KEY]
      if (cached) return 'ready'
      // No detected tools at all → nothing to merge
      if (state.detectedToolIds !== null && state.detectedToolIds.length === 0) {
        return 'no-sources'
      }
      return 'loading'
    },
  )
  const [mergeError, setMergeError] = useState<string | null>(null)
  const [conflicts] = useState<MergeConflict[]>([])
  const mergeFired = useRef(false)

  // The live merge result (from context, not local state — so approve/reject flows through)
  const mergeResult = getMergeResult(TIER_KEY)

  // ---------------------------------------------------------------------------
  // Derived state
  // ---------------------------------------------------------------------------

  const sections = mergeResult?.sections ?? []
  const parseError = sections.length === 1 && sections[0]?.id === 'parse-error'
  const pendingCount = sections.filter((s) => s.status === 'pending').length
  const canApply = sections.length > 0 && pendingCount === 0

  const toolCount = state.detectedToolIds?.length ?? 0
  const fileCount = state.scanResult?.fileCount ?? 0

  // ---------------------------------------------------------------------------
  // Run merge
  // ---------------------------------------------------------------------------

  const runMerge = useCallback(async () => {
    setMergePhase('loading')
    setMergeError(null)
    try {
      // Load legacy content fresh from disk: scan detected tool homes, then
      // read each home's text files via the Rust reader (T-24). Fresh reads
      // keep this resume-safe — paths are never stale wizard state.
      const toolIds = state.detectedToolIds ?? []
      const homes = await invoke<
        { toolId: string; globalPaths: string[]; perProjectPaths: string[] }[]
      >('scan_global_homes', { toolIds })
      const loaded: MergeSourceFile[] = []
      for (const home of homes) {
        const paths = [...home.globalPaths, ...home.perProjectPaths]
        if (paths.length === 0) continue
        const files = await invoke<MergeSourceFile[]>('read_legacy_content', {
          toolId: home.toolId,
          paths,
        })
        loaded.push(...files)
      }
      if (loaded.length === 0) {
        setMergePhase('no-sources')
        return
      }
      const result = await mergeForTier(WizardStep.MergeContent, loaded, {
        providerConnected: state.providerConnected,
      })
      setMergeResult(TIER_KEY, result)
      setMergePhase('ready')
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setMergeError(msg)
      setMergePhase('error')
    }
  }, [state.providerConnected, state.detectedToolIds, setMergeResult])

  // ---------------------------------------------------------------------------
  // Template apply helper (T-P3-E05-19)
  // ---------------------------------------------------------------------------

  /**
   * Apply selected templates as a pre-scaffold before AI merge.
   * Order: tier first, then stack, then shape (per CR-A guidance).
   * Non-destructive: dry_run=false, force=false (skip conflicting sections).
   * Graceful: on IPC error, sets templateApplyError and continues bare.
   *
   * @returns pre-scaffold content string, or null if nothing to apply/error.
   */
  const applySelectedTemplates = useCallback(async (): Promise<string | null> => {
    const ids = state.selectedTemplates
    if (ids.length === 0) return null
    setTemplateApplyError(null)
    try {
      let scaffoldContent = ''
      for (const templateId of ids) {
        const result = await invoke<ApplyResultIpc>('template_apply', {
          templateId,
          tier: TIER_KEY,
          dryRun: false,
          force: false,
        })
        // Accumulate sections that were applied (not skipped/conflicted)
        if (result.applied.length > 0) {
          // The Rust command writes to disk; we accumulate the section headings
          // as metadata for the AI merge prompt — the actual content comes from
          // the written file which mergeForTier reads via read_legacy_content.
          scaffoldContent += result.applied.map((s) => `## ${s}`).join('\n') + '\n'
        }
      }
      return scaffoldContent || null
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      setTemplateApplyError(msg)
      // Non-fatal: proceed with bare AI merge
      return null
    }
  }, [state.selectedTemplates])

  // ---------------------------------------------------------------------------
  // Template picker "Proceed to AI merge" handler
  // ---------------------------------------------------------------------------

  const handleProceedFromPicker = useCallback(async () => {
    setPickingTemplate(false)
    // Apply templates as scaffold before starting merge
    await applySelectedTemplates()
    // Kick off the merge (picker is now hidden)
    if (!mergeFired.current) {
      mergeFired.current = true
      void runMerge()
    }
  }, [applySelectedTemplates, runMerge])

  // Fire on mount (once) if no cached result AND not in picker mode
  useEffect(() => {
    if (pickingTemplate) return // Picker is shown first; merge fires on proceed
    if (mergeFired.current) return
    const cached = state.mergeResults[TIER_KEY]
    if (cached) {
      setMergePhase('ready')
      return
    }
    mergeFired.current = true
    void runMerge()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pickingTemplate])

  // ---------------------------------------------------------------------------
  // Section status change callback → WizardContext
  // ---------------------------------------------------------------------------

  const handleSectionStatusChange = useCallback(
    (sectionId: string, status: 'pending' | 'approved' | 'rejected' | 'edited', editedContent?: string) => {
      if (status === 'edited' && editedContent !== undefined) {
        editSection(TIER_KEY, sectionId, editedContent)
      } else if (status === 'approved') {
        approveSection(TIER_KEY, sectionId)
      } else if (status === 'rejected') {
        rejectSection(TIER_KEY, sectionId)
      }
    },
    [approveSection, rejectSection, editSection],
  )

  // ---------------------------------------------------------------------------
  // Approve all helper
  // ---------------------------------------------------------------------------

  const handleApproveAll = useCallback(() => {
    if (!mergeResult) return
    for (const section of mergeResult.sections) {
      if (section.status === 'pending') {
        approveSection(TIER_KEY, section.id)
      }
    }
  }, [mergeResult, approveSection])

  // ---------------------------------------------------------------------------
  // Apply to Cascade
  // ---------------------------------------------------------------------------

  const handleApply = useCallback(async () => {
    if (!mergeResult || !canApply) return
    setMergePhase('writing')

    // Build write payload: approved + edited sections only (skip rejected)
    const toWrite: MergeSection[] = mergeResult.sections.filter(
      (s) => s.status === 'approved' || s.status === 'edited',
    )

    try {
      await invoke('write_cascade_content', {
        tier: TIER_KEY,
        sections: toWrite.map((s) => ({
          id: s.id,
          title: s.title,
          content: s.status === 'edited' ? (s.editedContent ?? s.proposedContent) : s.proposedContent,
        })),
      })
      markComplete(WizardStep.MergeContent)
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setMergeError(msg)
      setMergePhase('ready')
    }
  }, [mergeResult, canApply, markComplete])

  // ---------------------------------------------------------------------------
  // Skip (no sources / error recovery)
  // ---------------------------------------------------------------------------

  const handleSkip = useCallback(() => {
    markComplete(WizardStep.MergeContent)
  }, [markComplete])

  // ---------------------------------------------------------------------------
  // Render: template picker (T-P3-E05-19) — shown before the AI merge step
  // ---------------------------------------------------------------------------

  if (pickingTemplate) {
    return (
      <div className={cn('flex h-full flex-col gap-0', className)}>
        {/* Header */}
        <div className="flex items-center gap-2 border-b border-border px-6 py-3">
          <Layers className="h-5 w-5 text-primary" aria-hidden="true" />
          <h2 className="text-base font-semibold text-foreground">
            Start from a template (optional)
          </h2>
        </div>

        <div className="flex flex-1 flex-col gap-4 overflow-y-auto px-6 py-4">
          <p className="text-sm text-muted-foreground">
            Pick one or more templates to use as a structural scaffold for your CASCADE.md.
            The AI merge will then fill in your project-specific content on top of that
            scaffold. You can skip this step and proceed with a blank slate.
          </p>

          {/* Template apply error (non-fatal — from a previous attempt) */}
          {templateApplyError && (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-md border border-amber-400/50 bg-amber-50/30 px-3 py-2 text-xs text-amber-800 dark:text-amber-200"
            >
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" aria-hidden="true" />
              <span>
                <span className="font-medium">Template apply warning:</span>{' '}
                {templateApplyError} — proceeding without scaffold.
              </span>
            </div>
          )}

          <TemplatePickerCompact
            entries={templateCatalog ?? []}
            selectedIds={state.selectedTemplates}
            onChange={setSelectedTemplates}
            isLoading={templateCatalogLoading}
            loadError={
              // Only expose error when catalog is empty AND errored
              templateCatalogError && templateCatalog?.length === 0
                ? templateCatalogError
                : null
            }
          />
        </div>

        {/* Action bar */}
        <div className="flex items-center justify-end gap-3 border-t border-border px-6 py-3">
          <button
            type="button"
            onClick={() => void handleProceedFromPicker()}
            className={cn(
              'flex items-center gap-1.5 rounded-md bg-primary px-4 py-2 text-sm font-medium',
              'text-primary-foreground hover:bg-primary/90',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'transition-colors',
            )}
          >
            {state.selectedTemplates.length > 0
              ? `Proceed with ${state.selectedTemplates.length} template${state.selectedTemplates.length !== 1 ? 's' : ''}`
              : 'Skip template, proceed to AI merge'}
          </button>
        </div>
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Render: loading
  // ---------------------------------------------------------------------------

  if (mergePhase === 'loading') {
    return (
      <div className={cn('flex h-full flex-col', className)}>
        <MergeLoadingState toolCount={toolCount} fileCount={fileCount} />
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Render: no sources
  // ---------------------------------------------------------------------------

  if (mergePhase === 'no-sources') {
    return (
      <div className={cn('flex h-full flex-col items-center justify-center gap-6 px-8 py-12', className)}>
        <div className="flex h-16 w-16 items-center justify-center rounded-full bg-muted">
          <GitMerge className="h-8 w-8 text-muted-foreground" aria-hidden="true" />
        </div>
        <div className="text-center">
          <h2 className="text-xl font-semibold text-foreground">No legacy content found</h2>
          <p className="mt-2 max-w-sm text-sm text-muted-foreground">
            Cascade will start with a clean configuration. You can add rules and preferences
            in the editor after setup.
          </p>
        </div>
        <button
          type="button"
          onClick={handleSkip}
          className={cn(
            'flex items-center gap-2 rounded-md bg-primary px-5 py-2.5 text-sm font-medium',
            'text-primary-foreground hover:bg-primary/90',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
            'transition-colors',
          )}
        >
          <SkipForward className="h-4 w-4" aria-hidden="true" />
          Continue
        </button>
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Render: error
  // ---------------------------------------------------------------------------

  if (mergePhase === 'error') {
    return (
      <div className={cn('flex h-full flex-col items-center justify-center gap-6 px-8 py-12', className)}>
        <div className="flex h-16 w-16 items-center justify-center rounded-full bg-destructive/10">
          <XCircle className="h-8 w-8 text-destructive" aria-hidden="true" />
        </div>
        <div className="text-center">
          <h2 className="text-xl font-semibold text-foreground">Merge failed</h2>
          {mergeError && (
            <p className="mt-1 max-w-sm text-sm text-muted-foreground font-mono break-all">
              {mergeError}
            </p>
          )}
        </div>
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={() => void runMerge()}
            className={cn(
              'flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm font-medium',
              'text-primary-foreground hover:bg-primary/90',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'transition-colors',
            )}
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            Retry
          </button>
          <button
            type="button"
            onClick={handleSkip}
            className={cn(
              'flex items-center gap-2 rounded-md border border-border px-4 py-2 text-sm font-medium',
              'text-muted-foreground hover:bg-muted/50',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'transition-colors',
            )}
          >
            <SkipForward className="h-3.5 w-3.5" aria-hidden="true" />
            Skip merge (manual edit later)
          </button>
        </div>
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Render: ready (DiffPanel + apply bar)
  // ---------------------------------------------------------------------------

  return (
    <div className={cn('flex h-full flex-col gap-0', className)}>
      {/* Header bar */}
      <div className="flex items-center justify-between border-b border-border px-6 py-3">
        <div className="flex items-center gap-2">
          <GitMerge className="h-5 w-5 text-primary" aria-hidden="true" />
          <h2 className="text-base font-semibold text-foreground">Review Merged Content</h2>
        </div>
        <div className="flex items-center gap-2">
          {/* Approve all helper */}
          {pendingCount > 0 && (
            <button
              type="button"
              onClick={handleApproveAll}
              className={cn(
                'flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-xs font-medium',
                'text-muted-foreground hover:bg-muted/50',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                'transition-colors',
              )}
              aria-label={`Approve all ${pendingCount} pending sections`}
            >
              <CheckCheck className="h-3.5 w-3.5" aria-hidden="true" />
              Approve all
            </button>
          )}
          {/* Apply button */}
          {pendingCount > 0 && (
            <span id="apply-pending-hint" className="sr-only">
              Review all sections to continue. {pendingCount} section{pendingCount !== 1 ? 's' : ''} pending.
            </span>
          )}
          <button
            type="button"
            onClick={() => void handleApply()}
            disabled={!canApply || mergePhase === 'writing'}
            aria-describedby={pendingCount > 0 ? 'apply-pending-hint' : undefined}
            title={pendingCount > 0 ? `Review all sections to continue (${pendingCount} pending)` : undefined}
            className={cn(
              'flex items-center gap-1.5 rounded-md bg-primary px-4 py-1.5 text-xs font-medium',
              'text-primary-foreground hover:bg-primary/90',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'disabled:cursor-not-allowed disabled:opacity-50',
              'transition-colors',
            )}
          >
            {mergePhase === 'writing' ? (
              <>
                <RefreshCw className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                Writing…
              </>
            ) : (
              'Apply to Cascade'
            )}
          </button>
        </div>
      </div>

      {/* Parse error banner */}
      {parseError && (
        <Alert variant="warning" className="mx-4 mt-3 rounded-md text-xs">
          <AlertTriangle className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
          <div>
            <p className="font-medium">Parse warning</p>
            <p className="mt-0.5 opacity-80">
              The AI response could not be structured into sections. Raw output is shown below.
              You can approve it as-is or retry the merge.
            </p>
          </div>
        </Alert>
      )}

      {/* Write error banner */}
      {mergeError && mergePhase === 'ready' && (
        <Alert variant="destructive" className="mx-4 mt-3 rounded-md text-xs">
          <XCircle className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
          <div>
            <p className="font-medium">Write failed</p>
            <p className="mt-0.5 opacity-80">{mergeError}</p>
          </div>
        </Alert>
      )}

      {/* DiffPanel */}
      {mergeResult && (
        <div className="flex-1 overflow-hidden px-4 pb-4 pt-3">
          <DiffPanel
            result={mergeResult}
            conflicts={conflicts}
            onSectionStatusChange={handleSectionStatusChange}
          />
        </div>
      )}
    </div>
  )
}
