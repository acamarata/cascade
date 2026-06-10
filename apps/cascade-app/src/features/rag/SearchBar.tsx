/**
 * Purpose: Search input + submit button for RAG queries.
 *   Submits on Enter key or button click.
 * Inputs:
 *   onSearch  — callback invoked with the trimmed query string.
 *   loading   — disables input while a query is in-flight.
 *   disabled  — optional additional disable flag (e.g. no index yet).
 * Outputs: <form> with an accessible labeled input.
 * Constraints:
 *   - aria-label on the input for a11y (CR-C requirement).
 *   - Does not fire onSearch on empty/whitespace queries.
 *   - Input is controlled (clears to '' after submit is possible, but
 *     spec doesn't require clear-on-submit; value stays for refinement).
 * SPORT: MASTER-COMPONENTS.md — SearchBar (T-P4-E01-27)
 */

import React, { useState } from 'react'
import { Search } from 'lucide-react'

interface SearchBarProps {
  onSearch: (query: string) => void
  loading?: boolean
  disabled?: boolean
}

export function SearchBar({ onSearch, loading = false, disabled = false }: SearchBarProps) {
  const [query, setQuery] = useState('')

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const trimmed = query.trim()
    if (trimmed) onSearch(trimmed)
  }

  return (
    <form onSubmit={handleSubmit} className="flex items-center gap-2" role="search">
      <div className="relative flex-1">
        <Search
          className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
          aria-hidden="true"
        />
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              const trimmed = query.trim()
              if (trimmed) onSearch(trimmed)
            }
          }}
          placeholder="Search indexed knowledge base…"
          aria-label="RAG search query"
          disabled={disabled || loading}
          className={[
            'h-9 w-full rounded-md border border-input bg-background pl-9 pr-3 text-sm',
            'placeholder:text-muted-foreground',
            'focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-1',
            'disabled:cursor-not-allowed disabled:opacity-50',
          ].join(' ')}
        />
      </div>

      <button
        type="submit"
        disabled={disabled || loading || !query.trim()}
        aria-label="Submit search"
        className={[
          'inline-flex h-9 items-center gap-1.5 rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground',
          'transition-colors hover:bg-primary/90',
          'focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-1',
          'disabled:cursor-not-allowed disabled:opacity-50',
        ].join(' ')}
      >
        {loading ? (
          <>
            <span
              className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-primary-foreground/30 border-t-primary-foreground"
              aria-hidden="true"
            />
            Searching…
          </>
        ) : (
          'Search'
        )}
      </button>
    </form>
  )
}
