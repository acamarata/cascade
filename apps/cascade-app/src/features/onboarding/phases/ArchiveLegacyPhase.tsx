/**
 * Purpose: Phase 7 — Archive legacy tool homes wizard panel.
 *
 * Inputs:
 *   - WizardContext via useWizard(): state.detectedToolIds, state.toolModes,
 *     state.archiveManifestPath, state.archivedTools, dispatch, updateState
 *
 * Outputs:
 *   - List of ArchiveToolCard — one per detected tool (skips .cascade/ itself)
 *   - Per-tool preflight validation (called on mount in background)
 *   - "Archive selected tools" button → ArchiveConfirmDialog per tool → progress → done
 *   - "Skip archiving" button — marks all Keep In Place, advances step
 *   - Persists archiveChoices, archivedTools, archiveManifestPath into WizardContext
 *     via UPDATE_ARCHIVE_STATUS + UPDATE_STATE dispatches
 *
 * Constraints:
 *   - Never auto-archives on mount.
 *   - .cascade/ directory is never shown as an archivable option.
 *   - Already-archived tools (from listArchivedTools on mount) show "Archived" badge
 *     without re-archiving.
 *   - "Archive selected tools" batches all archive-choice tools with individual
 *     confirm dialogs; each confirm/cancel is independent.
 *   - Per-tool errors show inline with Retry.
 *   - Continue gated: at least one tool archived OR user clicked Skip.
 *
 * SPORT: MASTER-COMPONENTS.md — ArchiveLegacyPhase — wizard step 7
 * Tasks: T-P3-E03-20, T-P3-E03-21
 */

import { useCallback, useEffect, useState } from 'react'
import { Archive, SkipForward } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { ToolId } from '@/lib/scanner/types'
import type { ArchiveValidation } from '@/lib/archive/types'
import {
  archiveLegacyTools,
  archivePreflight,
  listArchivedTools,
} from '@/lib/archive/archiveApi'
import { useWizard } from '@/features/onboarding/WizardContext'
import { WizardStep } from '@/features/onboarding/types'
import { ArchiveToolCard, type ArchiveChoice, type ArchiveCardState } from './ArchiveToolCard'
import { ArchiveConfirmDialog } from './ArchiveConfirmDialog'

// ---------------------------------------------------------------------------
// Per-tool runtime state
// ---------------------------------------------------------------------------

interface ToolState {
  validation: ArchiveValidation | null | undefined
  choice: ArchiveChoice
  cardState: ArchiveCardState
  archiveError?: string
  originalRoot: string
  archiveRoot: string
  fileCount: number
  sizeBytes: number
}

// ---------------------------------------------------------------------------
// Inline minimal alert (matches sibling phases — until E-01 shadcn wired)
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
        className,
      )}
    >
      {children}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Derive per-tool path defaults
// ---------------------------------------------------------------------------

/**
 * Compute expected original and archive roots for a toolId.
 * Actual resolved paths come from the Rust side; these are UI display estimates.
 *
 * WHY: The Rust archive command does the real resolution; these are best-effort
 * display values shown before/during the archive. Post-archive, ToolArchive.originalRoot
 * and archiveRoot replace them.
 */
function deriveToolPaths(toolId: ToolId): { originalRoot: string; archiveRoot: string } {
  const homeMap: Partial<Record<ToolId, string>> = {
    'claude-code': '~/.claude',
    opencode: '~/.opencode',
    codex: '~/.codex',
    cursor: '~/.cursor',
    aider: '~/.aider',
    windsurf: '~/.windsurf',
    antigravity: '~/.antigravity',
  }
  return {
    originalRoot: homeMap[toolId] ?? `~/.${toolId}`,
    archiveRoot: `~/.cascade/legacy/${toolId}`,
  }
}

// ---------------------------------------------------------------------------
// ArchiveLegacyPhase
// ---------------------------------------------------------------------------

export interface ArchiveLegacyPhaseProps {
  /** Optional className forwarded to the root element. */
  className?: string
}

/**
 * Wizard Phase 7: Archive Legacy Tool Homes.
 *
 * Lists all detected tools with Archive/Keep controls. Each archive action
 * requires explicit per-tool confirmation via ArchiveConfirmDialog.
 * Results are stored in WizardContext for downstream phases.
 */
export function ArchiveLegacyPhase({ className }: ArchiveLegacyPhaseProps) {
  const { state, dispatch, updateState, markComplete } = useWizard()
  const { detectedToolIds, archivedTools } = state

  // Per-tool runtime state map
  const [toolStateMap, setToolStateMap] = useState<Record<string, ToolState>>(() => {
    const map: Record<string, ToolState> = {}
    const tools = detectedToolIds ?? []
    for (const id of tools) {
      const { originalRoot, archiveRoot } = deriveToolPaths(id)
      map[id] = {
        validation: null, // null = loading
        choice: 'archive',
        cardState: archivedTools?.[id] ? 'done' : 'idle',
        originalRoot,
        archiveRoot,
        fileCount: 0,
        sizeBytes: 0,
      }
    }
    return map
  })

  // Which tool is showing the confirm dialog (one at a time)
  const [confirmingTool, setConfirmingTool] = useState<ToolId | null>(null)

  // Whether any archive has completed this session (gates Continue)
  const [anyArchived, setAnyArchived] = useState(false)

  // Whether user clicked Skip
  const [skipped, setSkipped] = useState(false)

  // Overall fetch error (for listArchivedTools)
  const [loadError, setLoadError] = useState<string | null>(null)

  const tools = (detectedToolIds ?? []) as ToolId[]

  // ---------------------------------------------------------------------------
  // On mount: load already-archived list + run preflights in background
  // ---------------------------------------------------------------------------

  useEffect(() => {
    let cancelled = false

    async function init() {
      // 1. Fetch already-archived tools from manifest
      try {
        const archived = await listArchivedTools()
        if (cancelled) return

        const alreadyArchivedIds = new Set(archived.map((a) => a.toolId))

        // Update state map with archived tool info
        setToolStateMap((prev) => {
          const next = { ...prev }
          for (const toolArchive of archived) {
            const id = toolArchive.toolId
            if (next[id]) {
              next[id] = {
                ...next[id],
                cardState: 'done',
                originalRoot: toolArchive.originalRoot,
                archiveRoot: toolArchive.archiveRoot,
                fileCount: toolArchive.files.length,
                sizeBytes: toolArchive.files.reduce((acc, f) => acc + f.sizeBytes, 0),
              }
            }
          }
          return next
        })

        // 2. Run preflights in background for non-archived tools
        const toValidate = tools.filter((id) => !alreadyArchivedIds.has(id))
        await Promise.allSettled(
          toValidate.map(async (id) => {
            try {
              const validation = await archivePreflight(id)
              if (cancelled) return
              setToolStateMap((prev) => {
                if (!prev[id]) return prev
                return { ...prev, [id]: { ...prev[id], validation } }
              })
            } catch {
              if (cancelled) return
              // On preflight error, mark validation as failed (ok: false, issues: [])
              setToolStateMap((prev) => {
                if (!prev[id]) return prev
                return {
                  ...prev,
                  [id]: { ...prev[id], validation: { ok: false, issues: [] } },
                }
              })
            }
          }),
        )
      } catch (err) {
        if (!cancelled) {
          setLoadError(String(err))
        }
      }
    }

    void init()
    return () => {
      cancelled = true
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // ---------------------------------------------------------------------------
  // Choice change
  // ---------------------------------------------------------------------------

  const handleChoiceChange = useCallback((toolId: ToolId, choice: ArchiveChoice) => {
    setToolStateMap((prev) => ({
      ...prev,
      [toolId]: { ...prev[toolId], choice },
    }))
  }, [])

  // ---------------------------------------------------------------------------
  // Archive flow: open confirm dialog
  // ---------------------------------------------------------------------------

  const handleArchiveRequest = useCallback((toolId: ToolId) => {
    setConfirmingTool(toolId)
  }, [])

  // ---------------------------------------------------------------------------
  // Archive flow: confirmed — run archive_legacy_tools
  // ---------------------------------------------------------------------------

  const handleConfirm = useCallback(
    async (toolId: ToolId) => {
      setConfirmingTool(null)

      // Set card to archiving state
      setToolStateMap((prev) => ({
        ...prev,
        [toolId]: { ...prev[toolId], cardState: 'archiving' },
      }))

      try {
        const result = await archiveLegacyTools(toolId)

        // Success — update card with real paths from result
        setToolStateMap((prev) => ({
          ...prev,
          [toolId]: {
            ...prev[toolId],
            cardState: 'done',
            originalRoot: result.originalRoot,
            archiveRoot: result.archiveRoot,
            fileCount: result.files.length,
            sizeBytes: result.files.reduce((acc, f) => acc + f.sizeBytes, 0),
          },
        }))

        // T-21: dispatch UPDATE_ARCHIVE_STATUS to WizardContext
        dispatch({
          type: 'UPDATE_ARCHIVE_STATUS',
          payload: { toolId, archived: true },
        })

        // Also update archiveManifestPath on first successful archive
        const manifestPath = `${result.archiveRoot.split('/legacy/')[0]}/legacy/manifest.json`
        if (!state.archiveManifestPath) {
          updateState({ archiveManifestPath: manifestPath })
        }

        setAnyArchived(true)
      } catch (err) {
        setToolStateMap((prev) => ({
          ...prev,
          [toolId]: {
            ...prev[toolId],
            cardState: 'error',
            archiveError: String(err),
          },
        }))
      }
    },
    [dispatch, updateState, state.archiveManifestPath],
  )

  // ---------------------------------------------------------------------------
  // Retry
  // ---------------------------------------------------------------------------

  const handleRetry = useCallback((toolId: ToolId) => {
    setToolStateMap((prev) => ({
      ...prev,
      [toolId]: { ...prev[toolId], cardState: 'idle', archiveError: undefined },
    }))
    // Re-run preflight
    void archivePreflight(toolId).then((validation) => {
      setToolStateMap((prev) => {
        if (!prev[toolId]) return prev
        return { ...prev, [toolId]: { ...prev[toolId], validation } }
      })
    })
  }, [])

  // ---------------------------------------------------------------------------
  // Skip archiving
  // ---------------------------------------------------------------------------

  function handleSkip() {
    // Mark all as Keep In Place in wizard context (archiveChoices not required per T-21 spec
    // but archiveManifestPath stays null — downstream handles it)
    setSkipped(true)
    markComplete(WizardStep.ArchiveLegacy)
  }

  // ---------------------------------------------------------------------------
  // Continue (when some tools archived)
  // ---------------------------------------------------------------------------

  function handleContinue() {
    markComplete(WizardStep.ArchiveLegacy)
  }

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const isEmpty = tools.length === 0
  const confirmingState = confirmingTool ? toolStateMap[confirmingTool] : null
  const canContinue = anyArchived || skipped || tools.every((id) => toolStateMap[id]?.cardState === 'done')

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className={cn('flex h-full flex-col gap-6 overflow-y-auto px-8 py-10', className)}>
      {/* Header */}
      <div className="flex items-center gap-3">
        <div className="flex h-10 w-10 items-center justify-center rounded-full bg-primary/10">
          <Archive className="h-5 w-5 text-primary" aria-hidden="true" />
        </div>
        <div>
          <h2 className="text-lg font-semibold text-foreground">Archive Legacy Tools</h2>
          <p className="text-sm text-muted-foreground">
            Move legacy tool homes to{' '}
            <span className="font-mono">~/.cascade/legacy/</span>. Files are never
            deleted and can be restored at any time.
          </p>
        </div>
      </div>

      {/* Load error */}
      {loadError && (
        <Alert variant="destructive">
          <p className="text-sm font-medium">Failed to load archive status</p>
          <p className="mt-1 text-xs">{loadError}</p>
        </Alert>
      )}

      {/* Empty state */}
      {isEmpty && (
        <Alert variant="default" className="border-muted">
          <p className="text-sm font-medium">No tools detected</p>
          <p className="mt-1 text-xs text-muted-foreground">
            No legacy tool homes were found in the scan. Continue to the next step.
          </p>
        </Alert>
      )}

      {/* Tool cards */}
      {!isEmpty && (
        <div className="flex flex-col gap-3">
          {tools.map((toolId) => {
            const ts = toolStateMap[toolId]
            if (!ts) return null
            return (
              <ArchiveToolCard
                key={toolId}
                toolId={toolId}
                originalRoot={ts.originalRoot}
                archiveRoot={ts.archiveRoot}
                fileCount={ts.fileCount}
                sizeBytes={ts.sizeBytes}
                validation={ts.validation}
                choice={ts.choice}
                alreadyArchived={ts.cardState === 'done'}
                archiveState={ts.cardState}
                archiveError={ts.archiveError}
                onChoiceChange={(choice) => handleChoiceChange(toolId, choice)}
                onArchive={() => handleArchiveRequest(toolId)}
                onRetry={() => handleRetry(toolId)}
              />
            )
          })}
        </div>
      )}

      {/* Non-destructive assurance note */}
      {!isEmpty && (
        <p className="text-xs text-muted-foreground">
          Cascade will never archive its own{' '}
          <span className="font-mono">~/.cascade/</span> directory — only the
          legacy tool homes listed above.
        </p>
      )}

      {/* Action buttons */}
      <div className="mt-auto flex items-center gap-3">
        {canContinue && !skipped ? (
          <button
            type="button"
            onClick={handleContinue}
            className="rounded-md bg-primary px-5 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            Continue
          </button>
        ) : null}

        {!skipped && (
          <button
            type="button"
            onClick={handleSkip}
            className="inline-flex items-center gap-2 rounded-md border border-border px-4 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            aria-label="Skip archiving and continue"
          >
            <SkipForward className="h-4 w-4" aria-hidden="true" />
            Skip archiving
          </button>
        )}

        {skipped && (
          <p className="text-sm text-muted-foreground">
            Archiving skipped. Use the Continue button in the footer to advance.
          </p>
        )}
      </div>

      {/* Per-tool confirm dialog (one at a time) */}
      {confirmingTool && confirmingState && (
        <ArchiveConfirmDialog
          open={true}
          toolId={confirmingTool}
          originalRoot={confirmingState.originalRoot}
          archiveRoot={confirmingState.archiveRoot}
          fileCount={confirmingState.fileCount}
          onConfirm={() => void handleConfirm(confirmingTool)}
          onCancel={() => setConfirmingTool(null)}
        />
      )}
    </div>
  )
}
