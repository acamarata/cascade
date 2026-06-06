/**
 * Purpose: Displays ideas and inbox items across all projects with client-side text filter.
 * Inputs: none — fetches /api/personal/ideas-inbox via usePersonal.
 * Outputs: Filterable list of items grouped by type badge (idea / inbox).
 * Constraints: Pure Tailwind (no shadcn/ui). Filter is client-side only, no API re-fetch.
 * SPORT: T-P3-E02-12 IdeasInboxPanel
 */
import { useState } from 'react'
import { usePersonal } from '@/hooks/usePersonal'
import type { IdeasInboxResponse } from '@/lib/api'

export function IdeasInboxPanel() {
  const { data, loading, error } = usePersonal<IdeasInboxResponse>('/api/personal/ideas-inbox')
  const [query, setQuery] = useState('')

  const filtered = data
    ? data.items.filter(
        (item) =>
          item.name.toLowerCase().includes(query.toLowerCase()) ||
          item.project.toLowerCase().includes(query.toLowerCase()),
      )
    : []

  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-foreground">Ideas &amp; Inbox</h2>
        {data && (
          <span className="text-xs text-muted-foreground">{data.total} total</span>
        )}
      </div>

      <input
        type="text"
        placeholder="Filter…"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        className="w-full rounded border border-border bg-background px-2 py-1 text-xs focus:outline-none focus:ring-1 focus:ring-primary"
      />

      {loading && (
        <p className="text-xs text-muted-foreground animate-pulse">Loading…</p>
      )}

      {error && (
        <p className="text-xs text-destructive">{error}</p>
      )}

      {data && filtered.length === 0 && !loading && (
        <p className="text-xs text-muted-foreground">
          {query ? 'No matches.' : 'No items found.'}
        </p>
      )}

      {filtered.length > 0 && (
        <ul className="divide-y divide-border text-xs max-h-64 overflow-y-auto">
          {filtered.map((item) => (
            <li key={item.path} className="py-1.5 flex items-start justify-between gap-2">
              <div className="flex items-center gap-1.5 min-w-0">
                <span
                  className={`shrink-0 rounded px-1 py-0.5 text-[10px] font-medium uppercase ${
                    item.type === 'idea'
                      ? 'bg-blue-100 text-blue-700 dark:bg-blue-900 dark:text-blue-300'
                      : 'bg-amber-100 text-amber-700 dark:bg-amber-900 dark:text-amber-300'
                  }`}
                >
                  {item.type}
                </span>
                <span className="truncate text-foreground">{item.name}</span>
              </div>
              <div className="flex flex-col items-end shrink-0 gap-0.5">
                <span className="text-muted-foreground">{item.project}</span>
                <span className="text-muted-foreground">{item.modified_at}</span>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
