/**
 * Purpose: Tab bar component for switching between MergeSection items within a DiffPanel.
 *   Renders one tab per section; highlights the active section and shows status badges.
 *
 * Inputs:
 *   - sections: MergeSection[] — all sections for the active MergeResult tier.
 *   - activeId: string — id of the currently selected section.
 *   - onSelect: (sectionId: string) => void — callback when user clicks a tab.
 *
 * Outputs: A horizontal tab strip with aria-selected attributes (WCAG 2.1 AA compliant).
 *
 * Constraints:
 *   - No WizardContext reads. Pure display + callback pattern.
 *   - Keyboard-accessible: tabs follow ARIA Tab pattern — roving tabindex + Left/Right arrows.
 *   - ≤300 lines per ASI Policy 3.
 *
 * SPORT: MASTER-COMPONENTS.md — SectionTabs — merge-engine/
 * Tasks: T-P3-E03-28, T-P3-E03-36 (a11y: ARIA tab keyboard pattern)
 */

import { useCallback, useRef } from 'react'
import { cn } from '@/lib/utils'
import { sectionStatusBadgeClass } from './diffPanelHelpers'
import type { MergeSection } from '@/features/onboarding/merge/types'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface SectionTabsProps {
  sections: MergeSection[]
  activeId: string
  onSelect: (sectionId: string) => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * Horizontal tab strip for the DiffPanel — one tab per MergeSection.
 * Fires onSelect(sectionId) when the user clicks or keyboard-activates a tab.
 *
 * Implements the ARIA Tab Pattern (roving tabindex + Left/Right arrow keys):
 * - Only the active tab is in the tab sequence (tabIndex=0); others are -1.
 * - Left/Right arrow keys move focus between tabs and activate them.
 * - Home/End jump to first/last tab.
 * WCAG 2.1 AA: keyboard navigable, focus-visible ring on every tab.
 */
export function SectionTabs({ sections, activeId, onSelect }: SectionTabsProps) {
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([])

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLButtonElement>, currentIndex: number) => {
      let nextIndex: number | null = null

      if (e.key === 'ArrowRight') {
        nextIndex = (currentIndex + 1) % sections.length
      } else if (e.key === 'ArrowLeft') {
        nextIndex = (currentIndex - 1 + sections.length) % sections.length
      } else if (e.key === 'Home') {
        nextIndex = 0
      } else if (e.key === 'End') {
        nextIndex = sections.length - 1
      }

      if (nextIndex !== null) {
        e.preventDefault()
        const section = sections[nextIndex]
        if (section) {
          onSelect(section.id)
          tabRefs.current[nextIndex]?.focus()
        }
      }
    },
    [sections, onSelect],
  )

  return (
    <div
      role="tablist"
      aria-label="Merge sections"
      className="flex gap-1 overflow-x-auto border-b border-border pb-px"
    >
      {sections.map((section, index) => {
        const isActive = section.id === activeId
        return (
          <button
            key={section.id}
            ref={(el) => { tabRefs.current[index] = el }}
            type="button"
            role="tab"
            id={`section-tab-${section.id}`}
            aria-selected={isActive}
            aria-controls={`section-panel-${section.id}`}
            tabIndex={isActive ? 0 : -1}
            onClick={() => onSelect(section.id)}
            onKeyDown={(e) => handleKeyDown(e, index)}
            className={cn(
              'flex items-center gap-1.5 whitespace-nowrap rounded-t-md px-3 py-2 text-sm font-medium',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'transition-colors',
              isActive
                ? 'border border-b-background border-border bg-background text-foreground'
                : 'text-muted-foreground hover:text-foreground hover:bg-accent',
            )}
          >
            <span>{section.title}</span>
            <span
              className={cn(
                'rounded-full px-1.5 py-0.5 text-xs font-medium',
                sectionStatusBadgeClass(section.status),
              )}
              aria-label={`Status: ${section.status}`}
            >
              {section.status}
            </span>
          </button>
        )
      })}
    </div>
  )
}
