/**
 * Purpose: Confirmation dialog for restoring an archived tool's legacy files.
 *   Shows file count, destination paths, and an overwrite checkbox before
 *   invoking restore_tool via archiveApi.
 *
 * Inputs:
 *   - archive: ToolArchive — manifest entry for the tool being restored.
 *   - onRestore: (result: RestoreResult) => void — called on successful restore.
 *   - onCancel: () => void — called when the user dismisses without restoring.
 *
 * Outputs: Modal dialog with confirm/cancel controls and post-restore result.
 *
 * Constraints:
 *   - overwriteExisting defaults to false (skip conflicts, report them).
 *   - Shows conflict count from RestoreResult after restore completes.
 *   - Does NOT auto-restore on mount — user must click Restore.
 *   - WCAG 2.1 AA: focus-visible rings, aria-modal, aria-labelledby.
 *
 * SPORT: MASTER-COMPONENTS.md — RestoreConfirmDialog — settings/ — T-P3-E03-22
 * Task: T-P3-E03-22
 */

import { useState } from 'react'
import { ToolArchive, RestoreResult } from '@/lib/archive/types'
import { restoreTool } from '@/lib/archive/archiveApi'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { AlertTriangle, CheckCircle2, Loader2, X } from 'lucide-react'

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface RestoreConfirmDialogProps {
  archive: ToolArchive
  onRestore: (result: RestoreResult) => void
  onCancel: () => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * Modal dialog that confirms and executes a restore_tool IPC call.
 *
 * Three render states:
 *   1. confirm — shows file list, overwrite checkbox, Restore/Cancel buttons.
 *   2. restoring — spinner while IPC is in-flight.
 *   3. done — shows result (restored / skipped / errors) with a Close button.
 */
export function RestoreConfirmDialog({ archive, onRestore, onCancel }: RestoreConfirmDialogProps) {
  const [overwrite, setOverwrite] = useState(false)
  const [phase, setPhase] = useState<'confirm' | 'restoring' | 'done'>('confirm')
  const [result, setResult] = useState<RestoreResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Display label for tool id (e.g. 'claude-code' -> 'Claude Code')
  const toolLabel = archive.toolId
    .split('-')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')

  async function handleRestore() {
    setPhase('restoring')
    setError(null)
    try {
      const res = await restoreTool(archive.toolId, overwrite)
      setResult(res)
      setPhase('done')
      onRestore(res)
    } catch (err) {
      setError(String(err))
      setPhase('confirm')
    }
  }

  return (
    /* Backdrop */
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40"
      role="dialog"
      aria-modal="true"
      aria-labelledby="restore-dialog-title"
    >
      <div className="relative w-full max-w-md rounded-lg border border-border bg-background p-6 shadow-xl">
        {/* Close button (confirm phase only) */}
        {phase !== 'restoring' && (
          <button
            type="button"
            onClick={onCancel}
            className={cn(
              'absolute right-4 top-4 rounded p-1 text-muted-foreground',
              'hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
            )}
            aria-label="Cancel restore"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        )}

        {/* ----------------------------------------------------------------- */}
        {/* Confirm phase                                                       */}
        {/* ----------------------------------------------------------------- */}
        {phase === 'confirm' && (
          <div className="space-y-4">
            <h2 id="restore-dialog-title" className="text-base font-semibold pr-8">
              Restore {toolLabel} legacy files?
            </h2>

            <p className="text-sm text-muted-foreground">
              This will move{' '}
              <span className="font-medium text-foreground">
                {archive.files.length} file
                {archive.files.length !== 1 ? 's' : ''}
              </span>{' '}
              from <code className="rounded bg-muted px-1 text-xs">{archive.archiveRoot}</code> back
              to their original paths. Cascade content is not affected.
            </p>

            {/* Destination root preview */}
            <div className="rounded-md border border-border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
              <span className="font-medium text-foreground">Destination root: </span>
              {archive.originalRoot}
            </div>

            {/* Overwrite checkbox */}
            <label className="flex cursor-pointer items-start gap-3">
              <input
                type="checkbox"
                checked={overwrite}
                onChange={(e) => setOverwrite(e.target.checked)}
                className="mt-0.5 h-4 w-4 rounded border-border focus-visible:ring-2 focus-visible:ring-ring"
              />
              <span className="text-sm">
                Overwrite existing files at original paths
                <span className="block text-xs text-muted-foreground mt-0.5">
                  Default OFF: conflicting files are skipped and reported.
                </span>
              </span>
            </label>

            {/* Inline error (IPC failure) */}
            {error && (
              <div
                role="alert"
                className="flex items-start gap-2 rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm text-destructive"
              >
                <AlertTriangle className="h-4 w-4 mt-0.5 flex-shrink-0" aria-hidden="true" />
                <span>{error}</span>
              </div>
            )}

            <div className="flex justify-end gap-2 pt-2">
              <Button variant="outline" onClick={onCancel} size="sm">
                Cancel
              </Button>
              <Button onClick={handleRestore} size="sm">
                Restore {archive.files.length} file{archive.files.length !== 1 ? 's' : ''}
              </Button>
            </div>
          </div>
        )}

        {/* ----------------------------------------------------------------- */}
        {/* Restoring phase                                                     */}
        {/* ----------------------------------------------------------------- */}
        {phase === 'restoring' && (
          <div className="flex flex-col items-center gap-4 py-4">
            <Loader2 className="h-8 w-8 animate-spin text-primary" aria-hidden="true" />
            <p className="text-sm text-muted-foreground">Restoring {toolLabel} files…</p>
          </div>
        )}

        {/* ----------------------------------------------------------------- */}
        {/* Done phase                                                          */}
        {/* ----------------------------------------------------------------- */}
        {phase === 'done' && result && (
          <div className="space-y-4">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-5 w-5 text-green-500" aria-hidden="true" />
              <h2 id="restore-dialog-title" className="text-base font-semibold">
                {toolLabel} restored
              </h2>
            </div>

            {/* Result summary */}
            <dl className="rounded-md border border-border bg-muted/30 px-3 py-3 text-sm space-y-1">
              <div className="flex justify-between">
                <dt className="text-muted-foreground">Restored</dt>
                <dd className="font-medium">
                  {result.restoredFiles} file{result.restoredFiles !== 1 ? 's' : ''}
                </dd>
              </div>
              {result.skippedConflicts > 0 && (
                <div className="flex justify-between">
                  <dt className="text-amber-600 dark:text-amber-400 flex items-center gap-1">
                    <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
                    Skipped (conflicts)
                  </dt>
                  <dd className="font-medium text-amber-600 dark:text-amber-400">
                    {result.skippedConflicts}
                  </dd>
                </div>
              )}
            </dl>

            {/* Per-file errors */}
            {result.errors.length > 0 && (
              <div className="rounded-md border border-destructive/50 bg-destructive/10 p-3 space-y-1">
                <p className="text-sm font-medium text-destructive">Errors</p>
                <ul className="list-disc list-inside text-xs text-destructive space-y-0.5">
                  {result.errors.map((e, i) => (
                    <li key={i}>{e}</li>
                  ))}
                </ul>
              </div>
            )}

            {result.skippedConflicts > 0 && (
              <p className="text-xs text-muted-foreground">
                {result.skippedConflicts} file{result.skippedConflicts !== 1 ? 's' : ''} already
                existed at the original path and were skipped. Re-run with &quot;Overwrite&quot;
                enabled to replace them.
              </p>
            )}

            <div className="flex justify-end pt-2">
              <Button onClick={onCancel} size="sm">
                Close
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
