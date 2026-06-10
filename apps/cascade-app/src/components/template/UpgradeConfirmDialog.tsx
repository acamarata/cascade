/**
 * Purpose: Confirmation dialog shown before upgrading an applied template.
 *   Displays diff summary (added/changed/deprecated section counts), explains
 *   the preserve-user-sections behaviour, and optionally shows a conflict list.
 *   On confirm, caller invokes template_upgrade IPC.
 * Inputs:
 *   open — controls dialog visibility
 *   templateId — human-readable template identifier
 *   oldVersion — currently applied version string
 *   newVersion — available newer version string
 *   addedSections — sections in new version absent from old
 *   changedSections — sections present in both with differing content
 *   deprecatedSections — sections removed in new version (kept with notice)
 *   conflictSections — sections with user edits that will be overwritten
 *   force — if true, overwrite even user-edited sections
 *   upgrading — loading state while IPC is in-flight
 *   onConfirm(force) — called when the user confirms
 *   onCancel — called when the user cancels
 * Outputs: Radix AlertDialog; focus-trapped; Esc closes; aria labels present.
 * Constraints:
 *   - Default force=false; user-edited sections shown as conflict warnings.
 *   - Dialog does NOT call IPC directly — caller owns that responsibility.
 * SPORT: MASTER-COMPONENTS.md — UpgradeConfirmDialog
 */

import React from 'react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { AlertTriangle } from 'lucide-react'

interface UpgradeConfirmDialogProps {
  open: boolean
  templateId: string
  oldVersion: string
  newVersion: string
  addedSections: string[]
  changedSections: string[]
  deprecatedSections: string[]
  /** Sections with user edits that will be overwritten (requires force to proceed). */
  conflictSections: string[]
  /** Whether to force-overwrite user-edited sections. Default false. */
  force?: boolean
  /** True while the upgrade IPC call is in-flight. */
  upgrading?: boolean
  /** Invoked with the final force value when user confirms. */
  onConfirm: (force: boolean) => void
  onCancel: () => void
}

/**
 * Renders a small pill list of section names.
 */
function SectionList({
  items,
  label,
  colorClass,
}: {
  items: string[]
  label: string
  colorClass: string
}): React.ReactElement | null {
  if (items.length === 0) return null
  return (
    <div className="mt-2">
      <p className={`text-xs font-medium ${colorClass} mb-1`}>{label}</p>
      <ul className="flex flex-wrap gap-1">
        {items.map((s) => (
          <li
            key={s}
            className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground"
          >
            {s}
          </li>
        ))}
      </ul>
    </div>
  )
}

/**
 * UpgradeConfirmDialog — review diff and confirm template upgrade.
 */
export function UpgradeConfirmDialog({
  open,
  templateId,
  oldVersion,
  newVersion,
  addedSections,
  changedSections,
  deprecatedSections,
  conflictSections,
  force = false,
  upgrading = false,
  onConfirm,
  onCancel,
}: UpgradeConfirmDialogProps): React.ReactElement {
  const totalChanged = addedSections.length + changedSections.length + deprecatedSections.length
  const hasConflicts = conflictSections.length > 0

  function handleConfirm() {
    onConfirm(force)
  }

  return (
    <Dialog open={open} onOpenChange={(isOpen) => { if (!isOpen) onCancel() }}>
      <DialogContent aria-label={`Upgrade ${templateId} confirmation`}>
        <DialogHeader>
          <DialogTitle>Upgrade template to v{newVersion}?</DialogTitle>
          <DialogDescription asChild>
            <div>
              <p className="text-sm text-muted-foreground">
                <strong className="text-foreground">{templateId}</strong> was applied at v{oldVersion}.
                v{newVersion} is available — {totalChanged} section{totalChanged !== 1 ? 's' : ''} will
                change.
              </p>
              <p className="mt-2 text-xs text-muted-foreground">
                Sections you have not edited are updated automatically. Sections you have edited
                manually are preserved unless you choose to force-overwrite them.
              </p>
            </div>
          </DialogDescription>
        </DialogHeader>

        {/* Diff summary */}
        <div className="rounded-md border border-border bg-muted/30 p-3">
          <SectionList
            items={addedSections}
            label={`${addedSections.length} added`}
            colorClass="text-green-700 dark:text-green-400"
          />
          <SectionList
            items={changedSections}
            label={`${changedSections.length} changed`}
            colorClass="text-blue-700 dark:text-blue-400"
          />
          <SectionList
            items={deprecatedSections}
            label={`${deprecatedSections.length} deprecated (preserved with notice)`}
            colorClass="text-muted-foreground"
          />
        </div>

        {/* Conflict warning */}
        {hasConflicts && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 p-3 dark:border-amber-700 dark:bg-amber-900/20"
          >
            <AlertTriangle
              className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400"
              aria-hidden="true"
            />
            <div>
              <p className="text-xs font-medium text-amber-800 dark:text-amber-300">
                {conflictSections.length} section{conflictSections.length !== 1 ? 's' : ''} you
                edited may be overwritten:
              </p>
              <ul className="mt-1 flex flex-wrap gap-1">
                {conflictSections.map((s) => (
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

        <DialogFooter>
          <Button variant="outline" onClick={onCancel} disabled={upgrading}>
            Cancel
          </Button>
          <Button
            onClick={handleConfirm}
            disabled={upgrading}
            aria-label={`Confirm upgrade ${templateId} to v${newVersion}`}
          >
            {upgrading ? 'Upgrading…' : 'Upgrade'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
