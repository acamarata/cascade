/**
 * Purpose: Displays CRD (Claude Relay Daemon) active and recent chains with expand/collapse.
 * Inputs: none — fetches /api/personal/crd-chains via usePersonal.
 * Outputs: Collapsible list of chains showing id, status badge, message count, and summary.
 * Constraints: Pure Tailwind (no shadcn/ui). Expand state is local — no API on expand.
 * SPORT: T-P3-E02-12 CRDChainsPanel
 */
import { useState } from 'react'
import { usePersonal } from '@/hooks/usePersonal'
import type { CrdChainsResponse } from '@/lib/api'

const STATUS_STYLES: Record<string, string> = {
  active: 'bg-green-100 text-green-700 dark:bg-green-900 dark:text-green-300',
  idle: 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-400',
  completed: 'bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400',
}

export function CRDChainsPanel() {
  const { data, loading, error } = usePersonal<CrdChainsResponse>('/api/personal/crd-chains')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  function toggle(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-foreground">CRD Chains</h2>
        {data && (
          <span className="text-xs text-muted-foreground">{data.chains.length} chains</span>
        )}
      </div>

      {loading && (
        <p className="text-xs text-muted-foreground animate-pulse">Loading…</p>
      )}

      {error && (
        <p className="text-xs text-destructive">{error}</p>
      )}

      {data && data.chains.length === 0 && (
        <p className="text-xs text-muted-foreground">No active chains.</p>
      )}

      {data && data.chains.length > 0 && (
        <ul className="divide-y divide-border text-xs">
          {data.chains.map((chain) => (
            <li key={chain.id}>
              <button
                onClick={() => toggle(chain.id)}
                className="w-full flex items-center justify-between py-1.5 text-left hover:bg-muted/50 rounded px-1 transition-colors"
              >
                <div className="flex items-center gap-1.5 min-w-0">
                  <span
                    className={`shrink-0 rounded px-1 py-0.5 text-[10px] font-medium uppercase ${STATUS_STYLES[chain.status] ?? STATUS_STYLES.idle}`}
                  >
                    {chain.status}
                  </span>
                  <span className="truncate font-mono text-foreground">{chain.id}</span>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  <span className="text-muted-foreground">{chain.message_count} msgs</span>
                  <span className="text-muted-foreground">{expanded.has(chain.id) ? '▲' : '▼'}</span>
                </div>
              </button>

              {expanded.has(chain.id) && (
                <div className="px-2 pb-2 pt-0.5 text-muted-foreground">
                  <p className="mb-0.5">{chain.summary || 'No summary available.'}</p>
                  <p className="text-[10px]">Last updated: {chain.last_updated}</p>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
