/**
 * Purpose: Displays recent conversation thread files from ~/.claude/projects with 30s refresh.
 * Inputs: none — fetches /api/personal/threads internally via usePersonal.
 * Outputs: List of thread file entries with name, project path, and relative modified time.
 * Constraints: Pure Tailwind (no shadcn/ui). Shows spinner on load, error state on failure.
 * SPORT: T-P3-E02-12 ThreadsPanel
 */
import { usePersonal } from '@/hooks/usePersonal'
import type { ThreadsResponse } from '@/lib/api'

export function ThreadsPanel() {
  const { data, loading, error } = usePersonal<ThreadsResponse>('/api/personal/threads')

  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-foreground">Threads</h2>
        {data && (
          <span className="text-xs text-muted-foreground">{data.total} total</span>
        )}
      </div>

      {loading && (
        <p className="text-xs text-muted-foreground animate-pulse">Loading…</p>
      )}

      {error && (
        <p className="text-xs text-destructive">{error}</p>
      )}

      {data && data.items.length === 0 && (
        <p className="text-xs text-muted-foreground">No threads found.</p>
      )}

      {data && data.items.length > 0 && (
        <ul className="divide-y divide-border text-xs">
          {data.items.map((item) => (
            <li key={item.path} className="py-1.5 flex justify-between gap-2">
              <span className="truncate text-foreground">{item.name}</span>
              <span className="shrink-0 text-muted-foreground">{item.modified_at}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
