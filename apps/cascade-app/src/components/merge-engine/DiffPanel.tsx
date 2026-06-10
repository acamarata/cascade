/**
 * Purpose: Core parallel diff view component for the Cascade onboarding wizard.
 *   Shows legacy source files (left) vs AI-proposed cascade content (right),
 *   section-by-section. Provides per-section approve/reject/edit controls.
 *
 * Inputs:
 *   - result: MergeResult — full AI merge result with all sections.
 *   - conflicts: MergeConflict[] — advisory conflict pairs (default []).
 *   - onSectionStatusChange: (sectionId, status, editedContent?) => void —
 *     callback fired whenever a section's approval state changes. T-29 wires
 *     this to WizardState via dispatch.
 *
 * Outputs: Two-column layout — SectionTabs top, SourceFileList left,
 *   ProposedContent right, section header with approve/reject/edit controls.
 *
 * Constraints:
 *   - No WizardContext reads. Pure props-only component.
 *   - Keyboard-accessible: all interactive elements focusable, WCAG 2.1 AA.
 *   - Responsive: at ≤800px left panel collapses to a toggle drawer.
 *   - ≤300 lines.
 *
 * SPORT: MASTER-COMPONENTS.md — DiffPanel — merge-engine/
 * Task: T-P3-E03-28
 */

import { useState, useCallback } from 'react'
import { CheckCircle2, XCircle, AlertTriangle, PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { MergeResult, MergeConflict, SectionStatus } from '@/features/onboarding/merge/types'
import { SectionTabs } from './SectionTabs'
import { SourceFileList } from './SourceFileList'
import { ProposedContent } from './ProposedContent'
import {
  nextSectionStatus,
  findConflictForSection,
  sectionBorderClass,
  sectionStatusBadgeClass,
} from './diffPanelHelpers'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface DiffPanelProps {
  /** Full AI merge result for one wizard tier. */
  result: MergeResult
  /** Advisory conflicts from conflictDetector (T-P3-E03-25). Default: []. */
  conflicts?: MergeConflict[]
  /**
   * Fired when the user changes a section's approval status or edits its content.
   *
   * @param sectionId     - The section being modified.
   * @param status        - The new SectionStatus after the action.
   * @param editedContent - Present only when status === 'edited'; the user's text.
   */
  onSectionStatusChange: (sectionId: string, status: SectionStatus, editedContent?: string) => void
}

// ---------------------------------------------------------------------------
// DiffPanel
// ---------------------------------------------------------------------------

/**
 * Section-by-section parallel diff view for wizard phase 4 (MergeContent)
 * and phase 6 (VerifyDiff). Composed of SectionTabs, SourceFileList, and
 * ProposedContent. Fires onSectionStatusChange for T-29 to dispatch to wizard state.
 */
export function DiffPanel({ result, conflicts = [], onSectionStatusChange }: DiffPanelProps) {
  const { sections } = result
  const [activeId, setActiveId] = useState<string>(sections[0]?.id ?? '')
  const [selectedSourcePath, setSelectedSourcePath] = useState<string | null>(null)
  const [leftOpen, setLeftOpen] = useState(true)

  const activeSection = sections.find((s) => s.id === activeId) ?? sections[0]

  // Clear highlight when switching sections
  const handleTabSelect = useCallback((sectionId: string) => {
    setActiveId(sectionId)
    setSelectedSourcePath(null)
  }, [])

  const handleApprove = useCallback(() => {
    if (!activeSection) return
    const next = nextSectionStatus(activeSection.status, 'approve')
    onSectionStatusChange(activeSection.id, next)
  }, [activeSection, onSectionStatusChange])

  const handleReject = useCallback(() => {
    if (!activeSection) return
    const next = nextSectionStatus(activeSection.status, 'reject')
    onSectionStatusChange(activeSection.id, next)
  }, [activeSection, onSectionStatusChange])

  const handleEdit = useCallback(
    (sectionId: string, content: string) => {
      onSectionStatusChange(sectionId, 'edited', content)
    },
    [onSectionStatusChange]
  )

  if (!activeSection) {
    return <p className="px-4 py-6 text-sm text-muted-foreground">No sections to review.</p>
  }

  const conflict = findConflictForSection(activeSection.id, conflicts)

  return (
    <div className="flex flex-col h-full overflow-hidden rounded-lg border border-border bg-background">
      {/* SR live region: announces section status changes to screen readers */}
      <div role="status" aria-live="polite" aria-atomic="true" className="sr-only">
        {activeSection.title}: {activeSection.status}
      </div>

      {/* Section tabs */}
      {sections.length > 1 && (
        <div className="px-3 pt-2">
          <SectionTabs sections={sections} activeId={activeId} onSelect={handleTabSelect} />
        </div>
      )}

      {/* Section header */}
      <div
        className={cn(
          'flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3',
          sectionBorderClass(activeSection.status),
          'border-l-4'
        )}
        role="tabpanel"
        id={`section-panel-${activeSection.id}`}
        aria-labelledby={`section-tab-${activeSection.id}`}
      >
        <div className="flex items-center gap-2 min-w-0">
          <h2 className="truncate text-sm font-semibold">{activeSection.title}</h2>
          <span
            className={cn(
              'rounded-full px-2 py-0.5 text-xs font-medium',
              sectionStatusBadgeClass(activeSection.status)
            )}
          >
            {activeSection.status}
          </span>
          {conflict && (
            <span
              className="flex items-center gap-1 rounded-full bg-orange-100 px-2 py-0.5 text-xs font-medium text-orange-800 dark:bg-orange-900/30 dark:text-orange-400"
              title={conflict.reason}
              role="alert"
              aria-live="polite"
            >
              <AlertTriangle className="h-3 w-3" aria-hidden="true" />
              Conflict
            </span>
          )}
        </div>

        {/* Approve / Reject controls */}
        <div className="flex items-center gap-1.5 shrink-0">
          <button
            type="button"
            onClick={handleApprove}
            aria-pressed={activeSection.status === 'approved'}
            className={cn(
              'flex items-center gap-1 rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              activeSection.status === 'approved'
                ? 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-400'
                : 'border border-border hover:bg-accent'
            )}
          >
            <CheckCircle2 className="h-3.5 w-3.5" aria-hidden="true" />
            Approve
          </button>
          <button
            type="button"
            onClick={handleReject}
            aria-pressed={activeSection.status === 'rejected'}
            className={cn(
              'flex items-center gap-1 rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              activeSection.status === 'rejected'
                ? 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-400'
                : 'border border-border hover:bg-accent'
            )}
          >
            <XCircle className="h-3.5 w-3.5" aria-hidden="true" />
            Reject
          </button>
        </div>
      </div>

      {/* Body: two-column layout */}
      <div className="flex flex-1 min-h-0 overflow-hidden">
        {/* Left panel: source files — collapsible at ≤800px */}
        <div
          className={cn(
            'flex flex-col border-r border-border bg-muted/20 transition-all duration-200',
            leftOpen ? 'w-64 shrink-0' : 'w-0 overflow-hidden',
            // On wide layouts, always show
            'max-[800px]:hidden'
          )}
          aria-hidden={!leftOpen}
          // When collapsed, prevent keyboard focus reaching hidden children
          {...(!leftOpen && { inert: '' })}
        >
          <div className="flex items-center justify-between px-3 py-2 border-b border-border">
            <span className="text-xs font-medium text-muted-foreground">Sources</span>
          </div>
          <div className="flex-1 overflow-y-auto p-2">
            <SourceFileList
              sourceFiles={activeSection.sourceFiles}
              selectedPath={selectedSourcePath}
              onFileSelect={setSelectedSourcePath}
            />
          </div>
        </div>

        {/* Responsive toggle button — visible only at ≤800px */}
        <button
          type="button"
          onClick={() => setLeftOpen((v) => !v)}
          aria-label={leftOpen ? 'Collapse sources panel' : 'Expand sources panel'}
          aria-expanded={leftOpen}
          className={cn(
            'hidden max-[800px]:flex',
            'shrink-0 items-center justify-center w-7 border-r border-border',
            'bg-muted/30 hover:bg-muted/60 transition-colors',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
          )}
        >
          {leftOpen ? (
            <PanelLeftClose className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          ) : (
            <PanelLeftOpen className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          )}
        </button>

        {/* Right panel: proposed content */}
        <div className="flex-1 min-w-0 overflow-hidden">
          <ProposedContent
            section={activeSection}
            highlightSourcePath={selectedSourcePath}
            onEdit={handleEdit}
          />
        </div>
      </div>
    </div>
  )
}
