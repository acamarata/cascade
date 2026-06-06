/**
 * Purpose: Shared layout for file-list panels (rules, references, memory, agents, skills).
 * Inputs: title, endpoint, emptyMessage.
 * Outputs: Card with search input + filtered list of FileEntry items.
 * Constraints: Filtering is purely client-side — no re-fetch on search input change.
 * SPORT: T-P3-E02-09 FileListPanel shared component
 */
import { useState } from 'react'
import { useGCI } from '@/hooks/useGCI'
import type { FileListResponse } from '@/lib/api'

interface Props {
  title: string
  endpoint: string
  emptyMessage?: string
}

export function FileListPanel({ title, endpoint, emptyMessage = 'No items found.' }: Props) {
  const { data, loading, error, refetch } = useGCI<FileListResponse>(endpoint)
  const [query, setQuery] = useState('')

  const items = data?.items ?? []
  const filtered = query.trim()
    ? items.filter(
        (i) =>
          i.name.toLowerCase().includes(query.toLowerCase()) ||
          i.path.toLowerCase().includes(query.toLowerCase()),
      )
    : items

  return (
    <div className="rounded-lg border border-border bg-card text-card-foreground p-4 flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <h2 className="font-semibold text-base">{title}</h2>
        {data && (
          <span className="text-xs text-muted-foreground">{data.total} total</span>
        )}
      </div>

      <input
        type="search"
        placeholder="Filter…"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
      />

      {loading && (
        <div className="flex items-center justify-center h-20 text-muted-foreground text-sm">
          <span className="animate-spin mr-2">⟳</span> Loading…
        </div>
      )}

      {error && !loading && (
        <div className="flex flex-col items-center gap-2 text-destructive text-sm py-4">
          <span>{error}</span>
          <button
            onClick={refetch}
            className="text-xs underline text-muted-foreground hover:text-foreground"
          >
            Retry
          </button>
        </div>
      )}

      {!loading && !error && filtered.length === 0 && (
        <p className="text-sm text-muted-foreground py-4 text-center">{emptyMessage}</p>
      )}

      {!loading && !error && filtered.length > 0 && (
        <ul className="divide-y divide-border text-sm max-h-64 overflow-y-auto">
          {filtered.map((item) => (
            <li key={item.path} className="py-2 flex flex-col gap-0.5">
              <span className="font-medium text-foreground truncate">{item.name}</span>
              <span className="text-xs text-muted-foreground truncate">{item.path}</span>
              {item.modified_at && (
                <span className="text-xs text-muted-foreground/60">{item.modified_at}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
