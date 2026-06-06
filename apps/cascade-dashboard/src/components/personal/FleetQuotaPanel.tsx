/**
 * Purpose: Shows per-account Claude/OC quota utilisation with live countdown to reset.
 * Inputs: none — fetches /api/personal/fleet-quota via usePersonal (30s refresh).
 * Outputs: Per-account cc_pct + oc_pct progress bars with models_available list and
 *          a 1-second local countdown derived from resets_at.
 * Constraints: Two independent timers — 1s setInterval for countdown display,
 *              usePersonal handles 30s API refresh. Pure Tailwind + ProgressBar.
 * SPORT: T-P3-E02-13 FleetQuotaPanel
 */
import { useState, useEffect } from 'react'
import { usePersonal } from '@/hooks/usePersonal'
import { ProgressBar } from '@/components/ui/ProgressBar'
import type { FleetQuotaResponse } from '@/lib/api'

/** Format seconds as mm:ss or h:mm:ss */
function formatCountdown(seconds: number): string {
  if (seconds <= 0) return '0:00'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  const mm = String(m).padStart(h > 0 ? 2 : 1, '0')
  const ss = String(s).padStart(2, '0')
  return h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`
}

/** Seconds remaining until ISO timestamp; 0 if already past. */
function secsUntil(iso: string | null): number {
  if (!iso) return 0
  return Math.max(0, Math.floor((new Date(iso).getTime() - Date.now()) / 1000))
}

export function FleetQuotaPanel() {
  const { data, loading, error, refetch } = usePersonal<FleetQuotaResponse>('/api/personal/fleet-quota')

  // Local countdown state — one entry per account keyed by account_id
  const [countdowns, setCountdowns] = useState<Record<string, number>>({})

  // Sync countdown seed from fresh API data
  useEffect(() => {
    if (!data) return
    const initial: Record<string, number> = {}
    for (const acct of data.accounts) {
      initial[acct.account_id] = secsUntil(acct.resets_at)
    }
    setCountdowns(initial)
  }, [data])

  // 1-second tick — decrement all countdowns independently
  useEffect(() => {
    const id = setInterval(() => {
      setCountdowns((prev) => {
        const next: Record<string, number> = {}
        for (const [k, v] of Object.entries(prev)) {
          next[k] = Math.max(0, v - 1)
        }
        return next
      })
    }, 1000)
    return () => clearInterval(id)
  }, [])

  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-foreground">Fleet Quota</h2>
        <div className="flex items-center gap-2">
          {data && (
            <span className="text-xs text-muted-foreground">
              updated {new Date(data.updated_at).toLocaleTimeString()}
            </span>
          )}
          <button
            onClick={() => refetch()}
            className="text-xs text-muted-foreground hover:text-foreground transition-colors"
            aria-label="Refresh fleet quota"
          >
            ↻
          </button>
        </div>
      </div>

      {loading && <p className="text-xs text-muted-foreground animate-pulse">Loading…</p>}
      {error && <p className="text-xs text-destructive">{error}</p>}

      {data && data.accounts.length === 0 && (
        <p className="text-xs text-muted-foreground">No accounts configured.</p>
      )}

      {data && data.accounts.map((acct) => {
        const secs = countdowns[acct.account_id] ?? secsUntil(acct.resets_at)
        return (
          <div key={acct.account_id} className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between">
              <span className="text-xs font-medium text-foreground font-mono">{acct.account_id}</span>
              <span className="text-xs text-muted-foreground tabular-nums">
                {acct.resets_at ? `resets in ${formatCountdown(secs)}` : 'no reset'}
              </span>
            </div>

            <div className="grid grid-cols-2 gap-2">
              <div>
                <div className="flex justify-between mb-0.5">
                  <span className="text-[10px] text-muted-foreground uppercase tracking-wide">CC</span>
                  <span className="text-[10px] text-muted-foreground tabular-nums">{acct.cc_pct}%</span>
                </div>
                <ProgressBar value={acct.cc_pct} label={`${acct.account_id} CC quota`} />
              </div>
              <div>
                <div className="flex justify-between mb-0.5">
                  <span className="text-[10px] text-muted-foreground uppercase tracking-wide">OC</span>
                  <span className="text-[10px] text-muted-foreground tabular-nums">{acct.oc_pct}%</span>
                </div>
                <ProgressBar value={acct.oc_pct} label={`${acct.account_id} OC quota`} />
              </div>
            </div>

            {acct.models_available.length > 0 && (
              <div className="flex flex-wrap gap-1 mt-0.5">
                {acct.models_available.map((m) => (
                  <span
                    key={m}
                    className="rounded bg-muted px-1 py-0.5 text-[10px] text-muted-foreground font-mono"
                  >
                    {m}
                  </span>
                ))}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
