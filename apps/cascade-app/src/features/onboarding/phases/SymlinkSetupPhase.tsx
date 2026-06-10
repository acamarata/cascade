/**
 * Purpose: Phase 8 — Symlink setup panel for the onboarding wizard.
 *
 * Inputs:
 *   WizardContext via useWizard() — reads wizard state for cascade-managed tool
 *   selections built up in prior steps.
 *
 * Outputs:
 *   - Preview table showing planned symlink operations (from → to).
 *   - Apply button invokes the Tauri `setup_symlinks` command.
 *   - Per-entry result rows: created / skipped / error with non-destructive
 *     messaging (skipped means "real file present — W-03 will handle it").
 *   - Calls updateState({ symlinkPlanApplied: true }) on success.
 *
 * Constraints:
 *   - Never invokes setup_symlinks without an explicit Apply click (no auto-run).
 *   - Skipped entries are not errors — they are non-destructively reported.
 *   - Empty plan → shows "Nothing to link" and allows skipping to next step.
 *   - Re-runnable: user can apply again after fixing a skipped entry externally.
 *
 * SPORT: MASTER-COMPONENTS.md — SymlinkSetupPhase
 * Tasks: T-P3-E03-14
 */

import { useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { useWizard } from '@/features/onboarding/WizardContext'
import type { WizardState } from '@/features/onboarding/types'
import { CheckCircle2, AlertCircle, SkipForward, Link, Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// Types matching the Rust structs in commands/symlinks.rs
// ---------------------------------------------------------------------------

/** Mirrors Rust SymlinkKind enum (serde lowercase). */
type SymlinkKind = 'replace' | 'sibling'

/** Mirrors Rust SymlinkEntry (serde camelCase). */
interface SymlinkEntry {
  source: string
  target: string
  kind: SymlinkKind
}

/** Mirrors Rust SymlinkPlan (serde camelCase). */
interface SymlinkPlan {
  entries: SymlinkEntry[]
}

/** Mirrors Rust SymlinkResult (serde camelCase). */
interface SymlinkResult {
  source: string
  target: string
  success: boolean
  error?: string
}

// ---------------------------------------------------------------------------
// Result row status derived from SymlinkResult
// ---------------------------------------------------------------------------

type EntryStatus = 'created' | 'skipped' | 'error'

function deriveStatus(result: SymlinkResult): EntryStatus {
  if (result.success) return 'created'
  if (result.error?.includes('real')) return 'skipped'
  return 'error'
}

// ---------------------------------------------------------------------------
// Inline minimal UI primitives (until shadcn alert is scaffolded in E-01)
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

function AlertTitle({ children }: { children: React.ReactNode }) {
  return <p className="mb-1 font-semibold">{children}</p>
}

function AlertDescription({ children }: { children: React.ReactNode }) {
  return <p className="text-sm text-muted-foreground">{children}</p>
}

// ---------------------------------------------------------------------------
// Path display helper — trims HOME prefix for readability
// ---------------------------------------------------------------------------

function trimHome(path: string): string {
  if (path.startsWith('/Users/') || path.startsWith('/home/')) {
    const parts = path.split('/')
    // Replace /Users/<name> or /home/<name> with ~
    return '~/' + parts.slice(3).join('/')
  }
  return path
}

// ---------------------------------------------------------------------------
// Derive the symlink plan from wizard state
// ---------------------------------------------------------------------------

/**
 * Build the SymlinkPlan from the wizard state.
 *
 * Reads `toolModes` (set by the ToolModes phase) and `detectedToolIds` to
 * determine which tools are cascade-managed and need a symlink. For now,
 * the per-tool source/target paths are placeholders — the concrete path
 * resolution will be wired in a follow-up ticket that adds a `symlinkPlan`
 * field built from the scanner output. An absent mode defaults to
 * 'cascade-managed' per spec.
 *
 * WHY: We must not block Phase 8 on unreleased path-resolution tickets.
 * An empty detected list renders "Nothing to link" cleanly.
 */
function buildPlan(state: WizardState): SymlinkPlan {
  const { detectedToolIds, toolModes } = state
  if (!detectedToolIds || detectedToolIds.length === 0) {
    return { entries: [] }
  }

  const entries: SymlinkEntry[] = detectedToolIds
    .filter((id) => {
      const mode = toolModes[id] ?? 'cascade-managed'
      return mode === 'cascade-managed'
    })
    .map((id) => ({
      // Concrete paths come from the scanner output; placeholders until wired.
      source: `~/.config/${id}/CLAUDE.md`,
      target: `~/.cascade/managed/${id}/CLAUDE.md`,
      kind: 'replace' as SymlinkKind,
    }))

  return { entries }
}

// ---------------------------------------------------------------------------
// SymlinkSetupPhase component
// ---------------------------------------------------------------------------

export interface SymlinkSetupPhaseProps {
  /** Optional className forwarded to the root element. */
  className?: string
}

/**
 * Wizard Phase 8: Symlink Setup.
 *
 * Shows a preview table of planned symlink operations and an Apply button.
 * After applying, shows per-entry results with non-destructive messaging.
 */
export function SymlinkSetupPhase({ className }: SymlinkSetupPhaseProps) {
  const { state, updateState } = useWizard()

  const plan = buildPlan(state)
  const isEmpty = plan.entries.length === 0

  const [isApplying, setIsApplying] = useState(false)
  const [results, setResults] = useState<SymlinkResult[] | null>(null)
  const [fatalError, setFatalError] = useState<string | null>(null)

  const hasApplied = results !== null
  const successCount = results?.filter((r) => r.success).length ?? 0
  const skippedCount =
    results?.filter((r) => !r.success && deriveStatus(r) === 'skipped').length ?? 0
  const errorCount = results?.filter((r) => !r.success && deriveStatus(r) === 'error').length ?? 0

  async function handleApply() {
    if (isApplying) return
    setIsApplying(true)
    setFatalError(null)
    try {
      const resultList = await invoke<SymlinkResult[]>('setup_symlinks', { plan })
      setResults(resultList)
      if (resultList.every((r) => r.success || deriveStatus(r) === 'skipped')) {
        updateState({ symlinkPlanApplied: true })
      }
    } catch (err) {
      setFatalError(String(err))
    } finally {
      setIsApplying(false)
    }
  }

  function handleSkip() {
    // Allow advancing past an empty plan or when no changes are needed.
    updateState({ symlinkPlanApplied: true } as Parameters<typeof updateState>[0])
  }

  return (
    <div className={cn('flex h-full flex-col gap-6 overflow-y-auto px-8 py-10', className)}>
      {/* Header */}
      <div className="flex items-center gap-3">
        <div className="flex h-10 w-10 items-center justify-center rounded-full bg-primary/10">
          <Link className="h-5 w-5 text-primary" aria-hidden="true" />
        </div>
        <div>
          <h2 className="text-lg font-semibold text-foreground">Symlink Setup</h2>
          <p className="text-sm text-muted-foreground">
            Cascade will create symlinks so your tools pick up the managed configuration
            automatically.
          </p>
        </div>
      </div>

      {/* Empty state */}
      {isEmpty && !hasApplied && (
        <Alert variant="default" className="border-muted">
          <AlertTitle>Nothing to link</AlertTitle>
          <AlertDescription>
            No cascade-managed tool selections were detected. You can continue to the next step.
          </AlertDescription>
        </Alert>
      )}

      {/* Preview table */}
      {!isEmpty && !hasApplied && (
        <div>
          <p className="mb-2 text-sm font-medium text-foreground">
            Planned operations ({plan.entries.length})
          </p>
          <div className="rounded-md border border-border overflow-hidden">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border bg-muted/40">
                  <th className="px-4 py-2 text-left font-medium text-muted-foreground">
                    Link location
                  </th>
                  <th className="px-4 py-2 text-left font-medium text-muted-foreground">
                    Points to
                  </th>
                  <th className="px-4 py-2 text-left font-medium text-muted-foreground">Kind</th>
                </tr>
              </thead>
              <tbody>
                {plan.entries.map((entry, i) => (
                  <tr
                    key={i}
                    className={cn(
                      'border-b border-border last:border-0',
                      i % 2 === 0 ? 'bg-background' : 'bg-muted/20'
                    )}
                  >
                    <td className="px-4 py-2 font-mono text-xs text-foreground break-all">
                      {trimHome(entry.source)}
                    </td>
                    <td className="px-4 py-2 font-mono text-xs text-muted-foreground break-all">
                      {trimHome(entry.target)}
                    </td>
                    <td className="px-4 py-2 capitalize text-xs text-muted-foreground">
                      {entry.kind}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Results table */}
      {hasApplied && results && results.length > 0 && (
        <div>
          <div className="mb-2 flex items-center gap-3 text-sm font-medium text-foreground">
            <span>Results</span>
            {successCount > 0 && (
              <span className="text-green-600 dark:text-green-400">{successCount} created</span>
            )}
            {skippedCount > 0 && (
              <span className="text-yellow-600 dark:text-yellow-400">{skippedCount} skipped</span>
            )}
            {errorCount > 0 && <span className="text-destructive">{errorCount} failed</span>}
          </div>
          <div className="rounded-md border border-border overflow-hidden">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border bg-muted/40">
                  <th className="px-4 py-2 text-left font-medium text-muted-foreground">
                    Link location
                  </th>
                  <th className="px-4 py-2 text-left font-medium text-muted-foreground">Status</th>
                  <th className="px-4 py-2 text-left font-medium text-muted-foreground">Note</th>
                </tr>
              </thead>
              <tbody>
                {results.map((result, i) => {
                  const status = deriveStatus(result)
                  return (
                    <tr
                      key={i}
                      className={cn(
                        'border-b border-border last:border-0',
                        i % 2 === 0 ? 'bg-background' : 'bg-muted/20'
                      )}
                    >
                      <td className="px-4 py-2 font-mono text-xs text-foreground break-all">
                        {trimHome(result.source)}
                      </td>
                      <td className="px-4 py-2">
                        {status === 'created' && (
                          <span className="inline-flex items-center gap-1 text-green-600 dark:text-green-400 text-xs font-medium">
                            <CheckCircle2 className="h-3.5 w-3.5" aria-hidden="true" />
                            Created
                          </span>
                        )}
                        {status === 'skipped' && (
                          <span className="inline-flex items-center gap-1 text-yellow-600 dark:text-yellow-400 text-xs font-medium">
                            <SkipForward className="h-3.5 w-3.5" aria-hidden="true" />
                            Skipped
                          </span>
                        )}
                        {status === 'error' && (
                          <span className="inline-flex items-center gap-1 text-destructive text-xs font-medium">
                            <AlertCircle className="h-3.5 w-3.5" aria-hidden="true" />
                            Error
                          </span>
                        )}
                      </td>
                      <td className="px-4 py-2 text-xs text-muted-foreground">
                        {status === 'skipped'
                          ? 'A real file exists here — the archive step will move it first.'
                          : (result.error ?? '')}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Empty plan applied */}
      {hasApplied && results && results.length === 0 && (
        <Alert variant="default">
          <AlertTitle>Nothing was linked</AlertTitle>
          <AlertDescription>The plan was empty. No filesystem changes were made.</AlertDescription>
        </Alert>
      )}

      {/* Fatal error */}
      {fatalError && (
        <Alert variant="destructive">
          <AlertTitle>Command failed</AlertTitle>
          <AlertDescription>{fatalError}</AlertDescription>
        </Alert>
      )}

      {/* Skipped-entries explanation */}
      {hasApplied && skippedCount > 0 && (
        <Alert variant="warning">
          <AlertTitle>Some links were skipped</AlertTitle>
          <AlertDescription>
            Cascade found existing files at {skippedCount} location
            {skippedCount === 1 ? '' : 's'} and did not overwrite them. The archive step (Phase 7)
            will move these files to a safe backup, after which you can re-run the symlink step.
          </AlertDescription>
        </Alert>
      )}

      {/* Action buttons */}
      <div className="mt-auto flex items-center gap-3">
        {isEmpty ? (
          <button
            type="button"
            onClick={handleSkip}
            className="rounded-md bg-primary px-5 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            Continue
          </button>
        ) : !hasApplied ? (
          <button
            type="button"
            onClick={handleApply}
            disabled={isApplying}
            className="inline-flex items-center gap-2 rounded-md bg-primary px-5 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {isApplying && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
            {isApplying ? 'Applying…' : 'Apply symlinks'}
          </button>
        ) : (
          <>
            {errorCount > 0 && (
              <button
                type="button"
                onClick={handleApply}
                disabled={isApplying}
                className="inline-flex items-center gap-2 rounded-md border border-border px-5 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted disabled:pointer-events-none disabled:opacity-60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {isApplying && <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />}
                Retry
              </button>
            )}
            <p className="text-sm text-muted-foreground">
              Use the Continue button in the footer to advance.
            </p>
          </>
        )}
      </div>
    </div>
  )
}
