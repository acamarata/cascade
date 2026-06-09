/**
 * Purpose: Card component representing one legacy tool in the Archive phase (step 7).
 *
 * Inputs:
 *   - toolId: kebab-case tool identifier
 *   - originalRoot: absolute path of tool's home directory
 *   - archiveRoot: resolved destination path inside ~/.cascade/legacy/
 *   - fileCount: number of files in the tool's home directory
 *   - sizeBytes: total size of all files in bytes
 *   - validation: preflight check result (loading | ok | issues | error)
 *   - choice: 'archive' | 'keep' — current user selection
 *   - alreadyArchived: true if tool is already in the manifest
 *   - archiveState: 'idle' | 'confirming' | 'archiving' | 'done' | 'error'
 *   - archiveError: error message string when archiveState === 'error'
 *   - onChoiceChange: called when user toggles archive/keep
 *   - onArchive: called when user confirms archiving this tool
 *   - onRetry: called when user retries after error
 *
 * Outputs:
 *   - Card with validation badge, toggle (Archive/Keep), Archive button,
 *     progress spinner, done badge, or inline error with retry.
 *
 * Constraints:
 *   - Never auto-archives on mount — user must click Archive button.
 *   - Already-archived tools show a badge and no Archive button.
 *   - .cascade/ directory is never shown as an archivable option (enforced upstream).
 *
 * SPORT: MASTER-COMPONENTS.md — ArchiveToolCard — wizard step 7
 * Tasks: T-P3-E03-20
 */

import { CheckCircle2, AlertCircle, Loader2, Archive, FolderOpen } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { ArchiveValidation } from '@/lib/archive/types'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type ArchiveChoice = 'archive' | 'keep'
export type ArchiveCardState = 'idle' | 'confirming' | 'archiving' | 'done' | 'error'

export interface ArchiveToolCardProps {
  toolId: string
  originalRoot: string
  archiveRoot: string
  fileCount: number
  sizeBytes: number
  /** null = loading; undefined = not fetched yet */
  validation: ArchiveValidation | null | undefined
  choice: ArchiveChoice
  alreadyArchived: boolean
  archiveState: ArchiveCardState
  archiveError?: string
  onChoiceChange: (choice: ArchiveChoice) => void
  /** Signal that the user clicked Archive — parent opens confirm dialog. */
  onArchive: () => void
  onRetry: () => void
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function displayName(toolId: string): string {
  return toolId
    .split('-')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

function trimHome(path: string): string {
  if (path.startsWith('/Users/') || path.startsWith('/home/')) {
    const parts = path.split('/')
    return '~/' + parts.slice(3).join('/')
  }
  return path
}

// ---------------------------------------------------------------------------
// Validation badge
// ---------------------------------------------------------------------------

function ValidationBadge({ validation }: { validation: ArchiveValidation | null | undefined }) {
  if (validation === null) {
    return (
      <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
        <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
        Checking…
      </span>
    )
  }
  if (validation === undefined) {
    return null
  }
  if (validation.ok) {
    return (
      <span className="inline-flex items-center gap-1 text-xs text-green-600 dark:text-green-400 font-medium">
        <CheckCircle2 className="h-3 w-3" aria-hidden="true" />
        Ready to archive
      </span>
    )
  }

  const fatals = validation.issues.filter(
    (i) => i.type === 'InsufficientDisk' || i.type === 'PermissionDenied',
  )
  return (
    <span className="inline-flex items-center gap-1 text-xs text-destructive font-medium">
      <AlertCircle className="h-3 w-3" aria-hidden="true" />
      {fatals.length > 0 ? `${fatals.length} blocking issue(s)` : `${validation.issues.length} warning(s)`}
    </span>
  )
}

// ---------------------------------------------------------------------------
// ArchiveToolCard
// ---------------------------------------------------------------------------

/**
 * Card showing one tool's archive status and controls.
 *
 * Shows preflight validation, Archive/Keep toggle, per-tool progress, and results.
 * Already-archived tools show a badge and no duplicate Archive button.
 */
export function ArchiveToolCard({
  toolId,
  originalRoot,
  archiveRoot,
  fileCount,
  sizeBytes,
  validation,
  choice,
  alreadyArchived,
  archiveState,
  archiveError,
  onChoiceChange,
  onArchive,
  onRetry,
}: ArchiveToolCardProps) {
  const isArchiving = archiveState === 'archiving'
  const isDone = archiveState === 'done' || alreadyArchived
  const isError = archiveState === 'error'

  const hasFatalIssue =
    validation != null &&
    validation !== undefined &&
    !validation.ok &&
    validation.issues.some(
      (i) => i.type === 'InsufficientDisk' || i.type === 'PermissionDenied',
    )

  return (
    <div
      className={cn(
        'rounded-lg border p-4 transition-colors',
        isDone
          ? 'border-green-200 bg-green-50/40 dark:border-green-900/50 dark:bg-green-900/10'
          : isError
          ? 'border-destructive/40 bg-destructive/5'
          : 'border-border bg-background',
      )}
    >
      {/* Header row */}
      <div className="flex items-start justify-between gap-3">
        {/* Tool name + meta */}
        <div className="flex items-center gap-3 min-w-0">
          <div className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md bg-muted">
            <FolderOpen className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          </div>
          <div className="min-w-0">
            <p className="text-sm font-medium text-foreground">{displayName(toolId)}</p>
            <p className="text-xs text-muted-foreground font-mono truncate">
              {trimHome(originalRoot)}
            </p>
          </div>
        </div>

        {/* Already-archived badge */}
        {isDone && (
          <span className="inline-flex flex-shrink-0 items-center gap-1 rounded-full bg-green-100 px-2.5 py-0.5 text-xs font-medium text-green-700 dark:bg-green-900/40 dark:text-green-400">
            <CheckCircle2 className="h-3 w-3" aria-hidden="true" />
            Archived
          </span>
        )}
      </div>

      {/* Stats + validation */}
      <div className="mt-3 flex flex-wrap items-center gap-4 text-xs text-muted-foreground">
        <span>{fileCount} {fileCount === 1 ? 'file' : 'files'}</span>
        <span>{formatBytes(sizeBytes)}</span>
        <ValidationBadge validation={validation} />
      </div>

      {/* Controls — only when not already done */}
      {!isDone && (
        <div className="mt-4 flex items-center justify-between gap-3">
          {/* Archive / Keep toggle */}
          <div
            role="radiogroup"
            aria-label={`Archive choice for ${displayName(toolId)}`}
            className="flex items-center gap-1 rounded-md border border-border p-0.5"
          >
            <button
              type="button"
              role="radio"
              aria-checked={choice === 'archive'}
              onClick={() => onChoiceChange('archive')}
              disabled={isArchiving}
              className={cn(
                'rounded px-3 py-1 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-60',
                choice === 'archive'
                  ? 'bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              Archive
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={choice === 'keep'}
              onClick={() => onChoiceChange('keep')}
              disabled={isArchiving}
              className={cn(
                'rounded px-3 py-1 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-60',
                choice === 'keep'
                  ? 'bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              Keep in place
            </button>
          </div>

          {/* Archive action / progress */}
          {choice === 'archive' && (
            <div className="flex items-center gap-2">
              {isArchiving && (
                <Loader2 className="h-4 w-4 animate-spin text-primary" aria-hidden="true" />
              )}
              {!isArchiving && !isError && (
                <button
                  type="button"
                  onClick={onArchive}
                  disabled={hasFatalIssue}
                  aria-label={`Archive ${displayName(toolId)}`}
                  className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <Archive className="h-3.5 w-3.5" aria-hidden="true" />
                  Archive
                </button>
              )}
            </div>
          )}
        </div>
      )}

      {/* Destination note (when archive choice selected and not done) */}
      {!isDone && choice === 'archive' && (
        <p className="mt-2 text-xs text-muted-foreground">
          Files will be moved to{' '}
          <span className="font-mono">{trimHome(archiveRoot)}</span>
          {' — '}restorable any time.
        </p>
      )}

      {/* Inline error */}
      {isError && (
        <div className="mt-3 flex items-start justify-between gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3">
          <div className="flex items-start gap-2 min-w-0">
            <AlertCircle
              className="h-4 w-4 flex-shrink-0 text-destructive mt-0.5"
              aria-hidden="true"
            />
            <p className="text-xs text-destructive break-words">{archiveError ?? 'Archive failed.'}</p>
          </div>
          <button
            type="button"
            onClick={onRetry}
            className="flex-shrink-0 rounded border border-destructive/50 px-2.5 py-1 text-xs font-medium text-destructive transition-colors hover:bg-destructive/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            Retry
          </button>
        </div>
      )}
    </div>
  )
}
