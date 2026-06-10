/**
 * Purpose: Right panel of DiffPanel — shows the AI-proposed markdown content for a
 *   MergeSection. Supports source-file highlight mode (yellow background when a source
 *   file is selected) and inline editing via double-click textarea.
 *
 * Inputs:
 *   - section: MergeSection — the section being displayed.
 *   - highlightSourcePath: string | null — selected source file path; triggers highlight CSS.
 *   - onEdit: (sectionId: string, content: string) => void — fired (debounced 300ms)
 *     whenever the user changes the inline edit textarea.
 *
 * Outputs: Read-only markdown pre with optional yellow highlight; double-click activates
 *   a full-height textarea for editing.
 *
 * Constraints:
 *   - No WizardContext reads. Props-only.
 *   - Debounce 300ms on onEdit per spec.
 *   - textarea initialises with current editedContent ?? proposedContent.
 *   - ≤300 lines.
 *
 * SPORT: MASTER-COMPONENTS.md — ProposedContent — merge-engine/
 * Task: T-P3-E03-28
 */

import { useState, useCallback, useRef, useEffect } from 'react'
import { Edit2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { MergeSection } from '@/features/onboarding/merge/types'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface ProposedContentProps {
  section: MergeSection
  highlightSourcePath: string | null
  onEdit: (sectionId: string, content: string) => void
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Returns the effective content to show: edited overrides proposed. */
function effectiveContent(section: MergeSection): string {
  return section.editedContent ?? section.proposedContent
}

// ---------------------------------------------------------------------------
// ProposedContent
// ---------------------------------------------------------------------------

/**
 * Displays the AI-proposed content for a section.
 * Double-click activates an inline textarea; changes fire onEdit (debounced 300ms).
 * When a source file is highlighted, applies a yellow tint to the entire content area
 * (full per-token attribution is a P4+ feature; current implementation highlights the pane).
 */
export function ProposedContent({
  section,
  highlightSourcePath,
  onEdit,
}: ProposedContentProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<string>(effectiveContent(section))
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  // Sync draft when section changes (tab switch)
  useEffect(() => {
    setDraft(effectiveContent(section))
    setEditing(false)
  }, [section.id]) // eslint-disable-line react-hooks/exhaustive-deps

  // Focus textarea on edit mode activation
  useEffect(() => {
    if (editing && textareaRef.current) {
      textareaRef.current.focus()
    }
  }, [editing])

  const handleDoubleClick = useCallback(() => {
    setEditing(true)
  }, [])

  const handleChange = useCallback(
    (e: React.ChangeEvent<HTMLTextAreaElement>) => {
      const value = e.target.value
      setDraft(value)
      if (debounceRef.current) clearTimeout(debounceRef.current)
      debounceRef.current = setTimeout(() => {
        onEdit(section.id, value)
      }, 300)
    },
    [section.id, onEdit],
  )

  const handleBlur = useCallback(() => {
    setEditing(false)
  }, [])

  const isHighlighted = highlightSourcePath !== null

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center justify-between px-3 py-2 border-b border-border">
        <span className="text-xs font-medium text-muted-foreground">Proposed Content</span>
        {!editing && (
          <button
            type="button"
            onClick={() => setEditing(true)}
            aria-label="Edit proposed content"
            className={cn(
              'flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground',
              'hover:bg-accent hover:text-foreground transition-colors',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
            )}
          >
            <Edit2 className="h-3 w-3" aria-hidden="true" />
            Edit
          </button>
        )}
      </div>

      <div
        className={cn(
          'flex-1 relative overflow-auto rounded-b-md',
          isHighlighted && 'ring-2 ring-yellow-400 ring-inset',
        )}
        onDoubleClick={handleDoubleClick}
        title={editing ? undefined : 'Double-click to edit'}
      >
        {editing ? (
          <textarea
            ref={textareaRef}
            value={draft}
            onChange={handleChange}
            onBlur={handleBlur}
            aria-label={`Edit content for section: ${section.title}`}
            className={cn(
              'absolute inset-0 w-full h-full resize-none',
              'bg-background font-mono text-sm text-foreground',
              'p-3 outline-none',
              'focus-visible:ring-2 focus-visible:ring-ring',
            )}
            spellCheck={false}
          />
        ) : (
          <pre
            data-source-path={highlightSourcePath ?? undefined}
            className={cn(
              'p-3 font-mono text-sm text-foreground whitespace-pre-wrap break-words min-h-full',
              isHighlighted && 'bg-yellow-50 dark:bg-yellow-900/10',
            )}
            aria-label={`Proposed content for section: ${section.title}`}
          >
            {effectiveContent(section)}
          </pre>
        )}
      </div>
    </div>
  )
}
