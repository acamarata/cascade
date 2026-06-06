/**
 * Purpose: Shows CC hooks registered in GCI settings — event, matcher, command, fire count.
 * Inputs: none.
 * Outputs: Table with columns: Event, Matcher, Command, Fire Count. 30s auto-refresh.
 * Constraints: Pure Tailwind table — no shadcn/ui Table (not installed in this project).
 * SPORT: T-P3-E02-09 HooksInspectorPanel
 */
import { useGCI } from '@/hooks/useGCI'
import type { HooksResponse } from '@/lib/api'

export function HooksInspectorPanel() {
  const { data, loading, error, refetch } = useGCI<HooksResponse>('/api/gci/hooks')
  const hooks = data?.hooks ?? []

  return (
    <div className="rounded-lg border border-border bg-card text-card-foreground p-4 flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <h2 className="font-semibold text-base">Hooks Inspector</h2>
        {data && (
          <span className="text-xs text-muted-foreground">{hooks.length} hooks</span>
        )}
      </div>

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

      {!loading && !error && hooks.length === 0 && (
        <p className="text-sm text-muted-foreground py-4 text-center">No hooks registered.</p>
      )}

      {!loading && !error && hooks.length > 0 && (
        <div className="overflow-auto max-h-64">
          <table className="w-full text-xs border-collapse">
            <thead>
              <tr className="border-b border-border text-left text-muted-foreground">
                <th className="pb-1.5 pr-3 font-medium whitespace-nowrap">Event</th>
                <th className="pb-1.5 pr-3 font-medium whitespace-nowrap">Matcher</th>
                <th className="pb-1.5 pr-3 font-medium min-w-0">Command</th>
                <th className="pb-1.5 font-medium text-right whitespace-nowrap">Fire Count</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border/50">
              {hooks.map((hook, idx) => (
                <tr key={idx} className="hover:bg-muted/30 transition-colors">
                  <td className="py-1.5 pr-3 font-mono whitespace-nowrap">{hook.event}</td>
                  <td className="py-1.5 pr-3 font-mono whitespace-nowrap text-muted-foreground">
                    {hook.matcher || '—'}
                  </td>
                  <td className="py-1.5 pr-3 font-mono truncate max-w-[12rem] text-foreground/80">
                    <span title={hook.command}>{hook.command}</span>
                  </td>
                  <td className="py-1.5 text-right tabular-nums">
                    <span
                      className={`${hook.fire_count > 0 ? 'text-green-600 dark:text-green-400' : 'text-muted-foreground'}`}
                    >
                      {hook.fire_count}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
