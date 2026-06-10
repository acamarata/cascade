/**
 * Purpose: Display a single MemoryEntry as a clickable card in the MemoryViewer.
 *   Shows project badge (coloured deterministically), title, 2-line excerpt,
 *   and relative mtime timestamp. Clicking calls onOpen(entry.path).
 * Inputs:
 *   entry  — MemoryEntry to display.
 *   onOpen — callback invoked with entry.path when the card is activated.
 * Outputs: A <button> card element.
 * Constraints:
 *   - Missing excerpt falls back gracefully (shows "No excerpt").
 *   - Excerpt is line-clamped to 2 lines via CSS.
 *   - Keyboard-accessible: Enter/Space activates via native button semantics.
 *   - Deterministic badge colour via projectBadgeColor(entry.project).
 * SPORT: MASTER-COMPONENTS.md — MemoryCard (T-P3-E06-12)
 */

import React from 'react'
import type { MemoryEntry } from '../../types/vault'
import { projectBadgeColor, projectLabel } from '../../features/memory/memoryFilters'
import { formatRelativeTime } from '../../lib/relativeTime'

interface MemoryCardProps {
  /** The memory entry to render. */
  entry: MemoryEntry
  /** Called with entry.path when the card is clicked or activated. */
  onOpen: (path: string) => void
}

/**
 * Clickable card for a single MemoryEntry.
 */
export function MemoryCard({ entry, onOpen }: MemoryCardProps): React.ReactElement {
  const badgeClasses = projectBadgeColor(entry.project)
  const label = projectLabel(entry.project)
  const relTime = formatRelativeTime(entry.mtime)
  const excerpt = entry.excerpt || 'No excerpt'

  return (
    <button
      type="button"
      onClick={() => onOpen(entry.path)}
      className={[
        'w-full text-left rounded-lg border border-border bg-card px-4 py-3',
        'hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
        'transition-colors cursor-pointer',
      ].join(' ')}
      aria-label={`Open ${entry.title} from ${label}`}
    >
      {/* Top row: project badge + timestamp */}
      <div className="flex items-center justify-between gap-2 mb-1.5">
        <span
          className={`inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium ${badgeClasses}`}
          title={entry.project}
        >
          {label}
        </span>
        <time
          dateTime={entry.mtime ? new Date(entry.mtime).toISOString() : ''}
          className="text-[11px] text-muted-foreground shrink-0"
        >
          {relTime}
        </time>
      </div>

      {/* Title */}
      <p className="text-sm font-semibold leading-snug text-foreground truncate">
        {entry.title}
      </p>

      {/* Excerpt — 2-line clamp */}
      <p className="mt-1 text-xs text-muted-foreground line-clamp-2 leading-relaxed">
        {excerpt}
      </p>
    </button>
  )
}
