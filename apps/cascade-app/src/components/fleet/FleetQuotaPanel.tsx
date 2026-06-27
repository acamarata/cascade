/**
 * Purpose: Per-account 5h/7d quota progress bars with live countdown timer.
 *   Ported from cascade-dashboard FleetQuotaPanel; reads from useAccounts() IPC
 *   instead of HTTP /api/personal/fleet-quota.
 * Inputs:  useAccounts() IPC poll (30s refresh).
 * Outputs: Per-account cc_pct (5h) + wk_pct (7d) progress bars with countdown.
 * Constraints:
 *   - utilization from IPC is 0–1 float; multiply by 100 for display percentage.
 *   - resets_at is a unix timestamp (seconds); null when unknown.
 *   - 1s countdown timer derived from usage.five_hour.resets_at.
 *   - No external ProgressBar component — inline Tailwind divs only.
 *   - No models_available field on AccountQuota (IPC type); skip it.
 * SPORT: MASTER-COMPONENTS.md — FleetQuotaPanel (fleet-02)
 */

import { useState, useEffect } from 'react'
import { RefreshCw } from 'lucide-react'
import { useAccounts } from '../../features/accounts/useAccounts'
import { accountLabel } from '../../features/accounts/types'
import type { AccountQuota } from '../../features/accounts/types'

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

/** Seconds remaining until unix timestamp; 0 if already past or null. */
function secsUntilUnix(unixTs: number | null | undefined): number {
  if (!unixTs) return 0
  return Math.max(0, Math.floor(unixTs - Date.now() / 1000))
}

/** Convert 0-1 utilization to 0-100 percentage, null-safe. */
function utilPct(util: number | null | undefined): number {
  if (util == null) return 0
  return Math.min(100, Math.round(util * 100))
}

/** Progress bar colour class based on percentage. */
function barColor(pct: number): string {
  if (pct > 80) return 'bg-red-500'
  if (pct > 50) return 'bg-yellow-400'
  return 'bg-green-500'
}

interface MiniBarProps {
  label: string
  pct: number
  ariaLabel: string
}

function MiniBar({ label, pct, ariaLabel }: MiniBarProps) {
  return (
    <div>
      <div className="flex justify-between mb-0.5">
        <span className="text-[10px] text-muted-foreground uppercase tracking-wide">{label}</span>
        <span className="text-[10px] text-muted-foreground tabular-nums">{pct}%</span>
      </div>
      <div
        className="h-1.5 w-full rounded-full bg-muted overflow-hidden"
        role="progressbar"
        aria-label={ariaLabel}
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
      >
        <div
          className={['h-full rounded-full transition-all', barColor(pct)].join(' ')}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}

interface AccountRowProps {
  acc: AccountQuota
  countdown: number
}

function AccountRow({ acc, countdown }: AccountRowProps) {
  const label = accountLabel(acc)
  const ccPct = utilPct(acc.usage?.five_hour?.utilization)
  const wkPct = utilPct(acc.usage?.seven_day?.utilization)
  const hasReset = Boolean(acc.usage?.five_hour?.resets_at)

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-foreground font-mono">
          {label}
          {acc.email ? (
            <span className="ml-1 text-[10px] text-muted-foreground font-normal">({acc.email})</span>
          ) : null}
        </span>
        <span className="text-xs text-muted-foreground tabular-nums">
          {hasReset ? `resets in ${formatCountdown(countdown)}` : 'no reset'}
        </span>
      </div>

      <div className="grid grid-cols-2 gap-2">
        <MiniBar label="5h" pct={ccPct} ariaLabel={`${label} 5-hour quota`} />
        <MiniBar label="7d" pct={wkPct} ariaLabel={`${label} 7-day quota`} />
      </div>
    </div>
  )
}

export function FleetQuotaPanel() {
  const { accounts, loading, error, refetch } = useAccounts()

  // Keyed by account string; value = seconds remaining
  const [countdowns, setCountdowns] = useState<Record<string, number>>({})

  // Sync countdown seeds when fresh data arrives
  useEffect(() => {
    if (!accounts.length) return
    const initial: Record<string, number> = {}
    for (const acc of accounts) {
      initial[acc.account] = secsUntilUnix(acc.usage?.five_hour?.resets_at)
    }
    setCountdowns(initial)
  }, [accounts])

  // 1-second tick
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
        <button
          type="button"
          onClick={() => void refetch()}
          className="text-muted-foreground hover:text-foreground transition-colors"
          aria-label="Refresh fleet quota"
        >
          <RefreshCw className={['h-3.5 w-3.5', loading ? 'animate-spin' : ''].join(' ')} aria-hidden="true" />
        </button>
      </div>

      {loading && accounts.length === 0 && (
        <p className="text-xs text-muted-foreground animate-pulse">Loading…</p>
      )}
      {error && <p className="text-xs text-destructive">{error}</p>}

      {!loading && !error && accounts.length === 0 && (
        <p className="text-xs text-muted-foreground">No accounts configured.</p>
      )}

      {accounts.map((acc) => (
        <AccountRow
          key={acc.account}
          acc={acc}
          countdown={countdowns[acc.account] ?? secsUntilUnix(acc.usage?.five_hour?.resets_at)}
        />
      ))}
    </div>
  )
}
