/**
 * Purpose: Pure helpers for DiffPanel — status transitions, conflict lookup,
 *   and source-attribution pairing logic. No React, no DOM.
 *
 * Inputs:  MergeSection arrays, SectionStatus values, MergeConflict arrays.
 * Outputs: Derived booleans/arrays used by DiffPanel sub-components.
 *
 * Constraints:
 *   - No side effects. Every function is deterministic and pure.
 *   - No any. Strict TypeScript.
 *
 * SPORT: MASTER-COMPONENTS.md — diffPanelHelpers (merge-engine/)
 * Task: T-P3-E03-28
 */

import type { MergeSection, MergeConflict, SectionStatus } from '@/features/onboarding/merge/types'

// ---------------------------------------------------------------------------
// Status transitions
// ---------------------------------------------------------------------------

/**
 * Given the current status of a section, compute the next status after applying
 * an action. Used by the status reducer in DiffPanel.
 *
 * @param current - Current SectionStatus of the section.
 * @param action  - One of 'approve' | 'reject' | 'edit'. 'edit' always moves to 'edited'.
 * @returns The new SectionStatus. Toggling the same action reverts to 'pending'.
 */
export function nextSectionStatus(
  current: SectionStatus,
  action: 'approve' | 'reject' | 'edit'
): SectionStatus {
  if (action === 'edit') return 'edited'
  if (action === 'approve') return current === 'approved' ? 'pending' : 'approved'
  // reject
  return current === 'rejected' ? 'pending' : 'rejected'
}

// ---------------------------------------------------------------------------
// Conflict lookup
// ---------------------------------------------------------------------------

/**
 * For a given sectionId, find a MergeConflict where that section is a participant.
 * Returns null if no conflict exists.
 *
 * @param sectionId - The section to look up.
 * @param conflicts - All conflicts produced by conflictDetector.
 */
export function findConflictForSection(
  sectionId: string,
  conflicts: MergeConflict[]
): MergeConflict | null {
  return conflicts.find((c) => c.sectionA.id === sectionId || c.sectionB.id === sectionId) ?? null
}

// ---------------------------------------------------------------------------
// Source-file → content attribution pairing
// ---------------------------------------------------------------------------

/**
 * Given a section and a selected source file path, returns the data-source-id
 * attribute value to use on highlighted spans.
 * Returns null when no source file is selected.
 *
 * @param section        - The MergeSection being viewed.
 * @param selectedPath   - Currently selected source file path (or null = none selected).
 */
export function resolveHighlightSourceId(
  section: MergeSection,
  selectedPath: string | null
): string | null {
  if (!selectedPath) return null
  const match = section.sourceFiles.find((sf) => sf.path === selectedPath)
  return match ? match.path : null
}

// ---------------------------------------------------------------------------
// Status-driven CSS class
// ---------------------------------------------------------------------------

/**
 * Returns the Tailwind border colour class for a section based on its status.
 *
 * @param status - Current SectionStatus.
 */
export function sectionBorderClass(status: SectionStatus): string {
  switch (status) {
    case 'approved':
      return 'border-green-500'
    case 'rejected':
      return 'border-red-500'
    case 'edited':
      return 'border-yellow-500'
    case 'pending':
    default:
      return 'border-border'
  }
}

/**
 * Returns the Tailwind background badge class for a section status.
 *
 * @param status - Current SectionStatus.
 */
export function sectionStatusBadgeClass(status: SectionStatus): string {
  switch (status) {
    case 'approved':
      return 'bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400'
    case 'rejected':
      return 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400'
    case 'edited':
      return 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400'
    case 'pending':
    default:
      return 'bg-muted text-muted-foreground'
  }
}
