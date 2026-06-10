/**
 * Purpose: Tag multi-select filter for the library panel.
 *   Shows a "Filter by tag" trigger with an active-count badge.
 *   Opens a Popover listing unique tags as checkboxes (AND logic).
 * Inputs:  availableTags[], selectedTags[], onToggleTag, onClearTags.
 * Outputs: Popover button + checkbox list.
 * Constraints:
 *   - Tag list derived from tab-scoped items (per spec: not global across all kinds).
 *   - Filter state lives in LibraryPanel (pure controlled component).
 *   - Uses Popover from src/components/ui/popover.tsx.
 * SPORT: MASTER-COMPONENTS.md — TagFilter (T-P3-E07-05)
 */

import React from 'react'
import { Filter } from 'lucide-react'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '../../components/ui/popover'

interface TagFilterProps {
  availableTags: string[]
  selectedTags: string[]
  onToggleTag: (tag: string) => void
  onClearTags: () => void
}

/**
 * Tag filter popover.
 */
export function TagFilter({
  availableTags,
  selectedTags,
  onToggleTag,
  onClearTags,
}: TagFilterProps): React.ReactElement {
  const activeCount = selectedTags.length

  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={
            activeCount > 0
              ? `Filter by tag — ${activeCount} active`
              : 'Filter by tag'
          }
          className="relative inline-flex h-8 items-center gap-1.5 rounded-md border border-input bg-background px-3 text-sm text-muted-foreground transition-colors hover:bg-accent/30 focus:outline-none focus:ring-2 focus:ring-ring"
        >
          <Filter className="h-3.5 w-3.5" aria-hidden="true" />
          <span>Filter by tag</span>
          {activeCount > 0 && (
            <span
              aria-label={`${activeCount} tags selected`}
              className="inline-flex h-4 min-w-[1rem] items-center justify-center rounded-full bg-primary px-1 text-[10px] font-semibold text-primary-foreground"
            >
              {activeCount}
            </span>
          )}
        </button>
      </PopoverTrigger>

      <PopoverContent align="start" className="w-56 p-3">
        <div className="mb-2 flex items-center justify-between">
          <span className="text-xs font-semibold text-foreground">Tags</span>
          {activeCount > 0 && (
            <button
              type="button"
              onClick={onClearTags}
              className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline focus:outline-none"
            >
              Clear
            </button>
          )}
        </div>

        {availableTags.length === 0 ? (
          <p className="text-xs text-muted-foreground">No tags available.</p>
        ) : (
          <ul role="list" className="max-h-48 space-y-1 overflow-y-auto">
            {availableTags.map((tag) => {
              const checked = selectedTags.includes(tag)
              const id = `tag-filter-${tag}`
              return (
                <li key={tag} role="listitem">
                  <label
                    htmlFor={id}
                    className="flex cursor-pointer items-center gap-2 rounded px-1 py-0.5 text-sm hover:bg-accent/30"
                  >
                    <input
                      id={id}
                      type="checkbox"
                      checked={checked}
                      onChange={() => onToggleTag(tag)}
                      className="h-3.5 w-3.5 rounded border-border accent-primary"
                    />
                    <span className="truncate text-foreground">{tag}</span>
                  </label>
                </li>
              )
            })}
          </ul>
        )}
      </PopoverContent>
    </Popover>
  )
}
