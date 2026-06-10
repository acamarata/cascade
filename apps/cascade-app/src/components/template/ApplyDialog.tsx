/**
 * Purpose: Dialog for applying a template to a target CASCADE.md file.
 *   Step 1: target path input + variables form → "Dry-run preview" button.
 *   Step 2: dry-run plan (conflicts + applied sections) → "Apply" button.
 *   Step 3: result feedback (success counts or error).
 * Inputs:
 *   open — visibility
 *   entry — selected TemplateEntryIpc
 *   onApply(targetPath, variables, dryRun) — caller owns IPC
 *   onClose — dismiss callback
 *   dryRunResult — result of a previous dry run (null until run)
 *   applyResult — final apply result (null until applied)
 *   applying — true while IPC in-flight
 *   error — error message string if IPC failed
 * Outputs: Accessible multi-step Dialog; focus-trapped; Esc closes.
 * Constraints:
 *   - Apply button MUST be disabled until dry-run has been executed (guards non-destructive
 *     sequencing — per ADR-014).
 *   - Variables form: derived from {{varName}} placeholders in template body (regex scan).
 *   - Default target path: "~/.cascade/CASCADE.md" (sensible global default for gci tier,
 *     "{projectRoot}/.cascade/CASCADE.md" for lower tiers — heuristic, user can edit).
 *   - Conflict list shown in amber; non-destructive messaging ("not overwritten").
 * SPORT: MASTER-COMPONENTS.md — ApplyDialog
 */

import React, { useEffect, useMemo, useState } from 'react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { AlertTriangle, CheckCircle } from 'lucide-react'
import type { ApplyResultIpc, TemplateEntryIpc } from '../../types/templates'

// ── Types ─────────────────────────────────────────────────────────────────────

export interface DryRunPreviewIpc {
  /** Sections that will be added to the target. */
  toApply: string[]
  /** Sections already present in the target (will be skipped). */
  conflicts: string[]
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/**
 * Scans a template body for {{varName}} placeholders.
 * Returns a deduplicated sorted list of variable names.
 */
export function extractTemplateVars(body: string): string[] {
  const matches = body.matchAll(/\{\{(\w+)\}\}/g)
  const vars = new Set<string>()
  for (const m of matches) {
    if (m[1]) vars.add(m[1])
  }
  return [...vars].sort()
}

/**
 * Returns a sensible default target path for the given template tier.
 */
export function defaultTargetPath(tier: string): string {
  switch (tier) {
    case 'gci':
      return '~/.cascade/CASCADE.md'
    case 'pci':
      return '~/.cascade/CASCADE.md'
    default:
      // Lower tiers (apc/ppc/prc/pac) default to project-relative
      return '{projectRoot}/.cascade/CASCADE.md'
  }
}

// ── Component ─────────────────────────────────────────────────────────────────

interface ApplyDialogProps {
  open: boolean
  entry: TemplateEntryIpc | null
  /** Called with (targetPath, variables, dryRun) — caller owns invoke. */
  onApply: (targetPath: string, variables: Record<string, string>, dryRun: boolean) => void
  onClose: () => void
  dryRunResult: DryRunPreviewIpc | null
  applyResult: ApplyResultIpc | null
  applying: boolean
  error: string | null
}

/**
 * ApplyDialog — guided template application: path+vars → dry-run → confirm → result.
 */
export function ApplyDialog({
  open,
  entry,
  onApply,
  onClose,
  dryRunResult,
  applyResult,
  applying,
  error,
}: ApplyDialogProps): React.ReactElement {
  const [targetPath, setTargetPath] = useState('')
  const [variables, setVariables] = useState<Record<string, string>>({})
  const [dryRunDone, setDryRunDone] = useState(false)

  // Reset local state whenever the dialog is opened for a new entry.
  useEffect(() => {
    if (open && entry) {
      setTargetPath(defaultTargetPath(entry.tier))
      setVariables({})
      setDryRunDone(false)
    }
  }, [open, entry])

  // Mark dry-run as done when we receive a result.
  useEffect(() => {
    if (dryRunResult !== null) {
      setDryRunDone(true)
    }
  }, [dryRunResult])

  const varNames = useMemo(() => (entry ? extractTemplateVars(entry.body) : []), [entry])

  function handleVarChange(name: string, value: string) {
    setVariables((prev) => ({ ...prev, [name]: value }))
    // Invalidate dry-run result when inputs change.
    setDryRunDone(false)
  }

  function handleTargetChange(value: string) {
    setTargetPath(value)
    setDryRunDone(false)
  }

  function handleDryRun() {
    if (!entry) return
    onApply(targetPath, variables, /* dryRun */ true)
  }

  function handleApply() {
    if (!entry || !dryRunDone) return
    onApply(targetPath, variables, /* dryRun */ false)
  }

  const canDryRun = targetPath.trim().length > 0 && !applying
  const canApply = dryRunDone && !applying && applyResult === null

  if (!entry) return <></>

  return (
    <Dialog
      open={open}
      onOpenChange={(isOpen) => {
        if (!isOpen) onClose()
      }}
    >
      <DialogContent aria-label={`Apply template ${entry.id}`} className="max-w-xl">
        <DialogHeader>
          <DialogTitle>Apply Template — {entry.id}</DialogTitle>
          <DialogDescription>
            Choose a target path, fill in any required variables, run a dry-run preview, then
            confirm to apply.
          </DialogDescription>
        </DialogHeader>

        {/* === Form section === */}
        {applyResult === null && (
          <div className="space-y-4">
            {/* Target path */}
            <div>
              <label
                htmlFor="apply-target-path"
                className="mb-1 block text-sm font-medium text-foreground"
              >
                Target path
              </label>
              <Input
                id="apply-target-path"
                type="text"
                value={targetPath}
                onChange={(e) => handleTargetChange(e.target.value)}
                placeholder="~/.cascade/CASCADE.md"
                aria-describedby="apply-target-help"
              />
              <p id="apply-target-help" className="mt-1 text-xs text-muted-foreground">
                Absolute or home-relative path to your CASCADE.md file.
              </p>
            </div>

            {/* Variables form */}
            {varNames.length > 0 && (
              <div>
                <p className="mb-2 text-sm font-medium text-foreground">Template variables</p>
                <div className="space-y-2">
                  {varNames.map((name) => (
                    <div key={name}>
                      <label
                        htmlFor={`var-${name}`}
                        className="mb-1 block text-xs font-medium text-muted-foreground"
                      >
                        {name}
                      </label>
                      <Input
                        id={`var-${name}`}
                        type="text"
                        value={variables[name] ?? ''}
                        onChange={(e) => handleVarChange(name, e.target.value)}
                        placeholder={`Value for {{${name}}}`}
                      />
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Dry-run button */}
            <Button
              variant="outline"
              onClick={handleDryRun}
              disabled={!canDryRun}
              aria-label="Preview changes without applying"
              className="w-full"
            >
              {applying && !dryRunDone ? 'Running preview…' : 'Dry-run preview'}
            </Button>

            {/* Dry-run result */}
            {dryRunResult !== null && (
              <div
                aria-live="polite"
                aria-label="Dry-run preview results"
                className="rounded-md border border-border bg-muted/30 p-3 text-sm"
              >
                <p className="mb-2 font-medium text-foreground">Preview:</p>
                {dryRunResult.toApply.length > 0 && (
                  <div className="mb-2">
                    <p className="text-xs font-medium text-green-700 dark:text-green-400">
                      {dryRunResult.toApply.length} section
                      {dryRunResult.toApply.length !== 1 ? 's' : ''} will be added
                    </p>
                    <ul className="mt-1 flex flex-wrap gap-1">
                      {dryRunResult.toApply.map((s) => (
                        <li
                          key={s}
                          className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground"
                        >
                          {s}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
                {dryRunResult.conflicts.length > 0 && (
                  <div
                    role="alert"
                    className="mt-2 flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 p-2 dark:border-amber-700 dark:bg-amber-900/20"
                  >
                    <AlertTriangle
                      className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400"
                      aria-hidden="true"
                    />
                    <div>
                      <p className="text-xs font-medium text-amber-800 dark:text-amber-300">
                        {dryRunResult.conflicts.length} section
                        {dryRunResult.conflicts.length !== 1 ? 's' : ''} already exist and will not
                        be overwritten:
                      </p>
                      <ul className="mt-1 flex flex-wrap gap-1">
                        {dryRunResult.conflicts.map((s) => (
                          <li
                            key={s}
                            className="rounded bg-amber-100 px-1.5 py-0.5 font-mono text-[11px] text-amber-900 dark:bg-amber-800/40 dark:text-amber-200"
                          >
                            {s}
                          </li>
                        ))}
                      </ul>
                    </div>
                  </div>
                )}
              </div>
            )}

            {/* Error */}
            {error !== null && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}
          </div>
        )}

        {/* === Success result === */}
        {applyResult !== null && (
          <div
            aria-live="polite"
            aria-label="Apply result"
            className="flex flex-col items-center gap-3 py-4 text-center"
          >
            <CheckCircle
              className="h-10 w-10 text-green-600 dark:text-green-400"
              aria-hidden="true"
            />
            <p className="text-sm font-medium text-foreground">Template applied successfully.</p>
            <div className="text-xs text-muted-foreground space-y-0.5">
              <p>
                {applyResult.applied.length} section{applyResult.applied.length !== 1 ? 's' : ''}{' '}
                added
              </p>
              {applyResult.conflicts.length > 0 && (
                <p>
                  {applyResult.conflicts.length} conflict
                  {applyResult.conflicts.length !== 1 ? 's' : ''} skipped (existing content
                  preserved)
                </p>
              )}
              {applyResult.skipped.length > 0 && (
                <p>
                  {applyResult.skipped.length} section{applyResult.skipped.length !== 1 ? 's' : ''}{' '}
                  unchanged
                </p>
              )}
            </div>
          </div>
        )}

        <DialogFooter>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={applying}
            data-testid="apply-dialog-close-cancel"
          >
            {applyResult !== null ? 'Close' : 'Cancel'}
          </Button>
          {applyResult === null && (
            <Button
              onClick={handleApply}
              disabled={!canApply}
              aria-label="Apply template to target file"
              aria-describedby={!dryRunDone ? 'apply-dry-run-required' : undefined}
            >
              {applying && dryRunDone ? 'Applying…' : 'Apply'}
            </Button>
          )}
          {!dryRunDone && (
            <span id="apply-dry-run-required" className="sr-only">
              Run a dry-run preview before applying.
            </span>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
