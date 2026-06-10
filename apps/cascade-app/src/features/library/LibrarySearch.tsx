/**
 * Purpose: Search input for the library panel — filters items in real time.
 * Inputs:  searchQuery string, onSearchChange callback.
 * Outputs: Controlled <input> with a Search icon.
 * Constraints:
 *   - No debounce (library is small per spec).
 *   - Filter state lives in LibraryPanel (this component is pure controlled).
 * SPORT: MASTER-COMPONENTS.md — LibrarySearch (T-P3-E07-05)
 */

import React from 'react'
import { Search } from 'lucide-react'

interface LibrarySearchProps {
  searchQuery: string
  onSearchChange: (value: string) => void
}

/**
 * Search input with a leading Search icon.
 */
export function LibrarySearch({
  searchQuery,
  onSearchChange,
}: LibrarySearchProps): React.ReactElement {
  return (
    <div className="relative flex items-center">
      <Search
        className="pointer-events-none absolute left-2.5 h-3.5 w-3.5 text-muted-foreground"
        aria-hidden="true"
      />
      <input
        type="search"
        role="searchbox"
        aria-label="Search library items"
        placeholder="Search…"
        value={searchQuery}
        onChange={(e) => onSearchChange(e.target.value)}
        className="h-8 w-48 rounded-md border border-input bg-background pl-8 pr-3 text-sm placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring"
      />
    </div>
  )
}
