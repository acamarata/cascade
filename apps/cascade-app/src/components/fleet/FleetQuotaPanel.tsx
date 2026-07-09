/**
 * Purpose: Per-account 5h/7d quota progress bars with live countdown timer,
 *   plus a compact "Needs sign-in" row for unauthenticated accounts.
 *   Ported from cascade-dashboard FleetQuotaPanel; reads from useAccounts() IPC
 *   instead of HTTP /api/personal/fleet-quota.
 * Inputs:  useAccounts() IPC poll (4s refresh) — accounts (authenticated only)
 *   + needsAuth (roster entries missing valid credentials).
 * Outputs: Per-account cc_pct (5h) + wk_pct (7d) progress bars with countdown,
 *   plus a re-auth affordance for accounts that need sign-in.
 * Constraints:
 *   - utilization from IPC is ALREADY a 0–100 percentage — never multiply by
 *     100 again (that bug clamped small utilizations, e.g. 8%, to a false
 *     100% once the value was re-scaled and capped).
 *   - resets_at is a unix timestamp (seconds); null when unknown.
 *   - 1s countdown timer derived from usage.five_hour.resets_at.
 *   - No external ProgressBar component — inline Tailwind divs only.
 *   - No models_available field on AccountQuota (IPC type); skip it.
 * SPORT: MASTER-COMPONENTS.md — FleetQuotaPanel (fleet-02)
 */

import { useState, useEffect, useCallback } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { RefreshCw, LogIn } from 'lucide-react'
import { useAccounts } from '../../features/accounts/useAccounts'
import { accountLabel, canReauth } from '../../features/accounts/types'
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

/** Clamp a 0-100 utilization percentage for display, null-safe. */
function utilPct(util: number | null | undefined): number {
  if (util == null) return 0
  return Math.min(100, Math.max(0, Math.round(util)))
}

/** Progress bar colour class based on percentage. */
function barColor(pct: number): string {
  if (pct > 80) return 'bg-rose-500 shadow-[0_0_8px_rgba(244,63,94,0.35)]'
  if (pct > 50) return 'bg-amber-500 shadow-[0_0_8px_rgba(245,158,11,0.35)]'
  return 'bg-emerald-500 shadow-[0_0_8px_rgba(16,185,129,0.35)]'
}

function getProviderBadgeStyles(provider: string): string {
  switch (provider?.toLowerCase()) {
    case 'claude':
      return 'bg-amber-500/10 text-amber-500 border border-amber-500/20'
    case 'codex':
      return 'bg-blue-500/10 text-blue-500 border border-blue-500/20'
    case 'gemini':
      return 'bg-violet-500/10 text-violet-500 border border-violet-500/20'
    case 'opencode':
      return 'bg-emerald-500/10 text-emerald-500 border border-emerald-500/20'
    default:
      return 'bg-muted text-muted-foreground border border-border'
  }
}

interface MiniBarProps {
  label: string
  pct: number
  ariaLabel: string
}

function MiniBar({ label, pct, ariaLabel }: MiniBarProps) {
  return (
    <div className="space-y-1">
      <div className="flex justify-between items-center">
        <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">{label}</span>
        <span className="text-[10px] font-bold text-foreground/80 tabular-nums">{pct}%</span>
      </div>
      <div
        className="h-2 w-full rounded-full bg-muted/60 overflow-hidden"
        role="progressbar"
        aria-label={ariaLabel}
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
      >
        <div
          className={['h-full rounded-full transition-all duration-500 ease-out', barColor(pct)].join(' ')}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}

/** Small colored account-id badge, matching the AccountsPage table convention. */
function AccountBadge({ acc }: { acc: AccountQuota }) {
  return (
    <span className={['rounded px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider', getProviderBadgeStyles(acc.provider)].join(' ')}>
      {accountLabel(acc)}
    </span>
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
    <div className="flex flex-col gap-3 rounded-lg border border-border/40 bg-muted/20 hover:bg-muted/40 hover:border-border/80 p-3 transition-all duration-200 shadow-sm">
      <div className="flex items-center justify-between">
        <span className="flex items-center gap-2 text-xs font-medium text-foreground min-w-0">
          <AccountBadge acc={acc} />
          {acc.email ? <span className="text-xs text-muted-foreground/70 font-normal truncate max-w-[140px]" title={acc.email}>{acc.email}</span> : null}
        </span>
        <span className="text-[10px] font-medium text-muted-foreground/80 bg-muted/60 px-2 py-0.5 rounded-full tabular-nums">
          {hasReset ? `resets in ${formatCountdown(countdown)}` : 'no reset'}
        </span>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <MiniBar label="5h" pct={ccPct} ariaLabel={`${label} 5-hour quota`} />
        <MiniBar label="7d" pct={wkPct} ariaLabel={`${label} 7-day quota`} />
      </div>
    </div>
  )
}

interface NeedsAuthRowProps {
  acc: AccountQuota
  busy: boolean
  onReauth: (accountId: string) => void
}

/** Compact row for a roster account that isn't currently authenticated. */
function NeedsAuthRow({ acc, busy, onReauth }: NeedsAuthRowProps) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-lg border border-dashed border-amber-500/20 bg-amber-500/5 p-3 transition-all duration-200 hover:border-amber-500/40">
      <span className="flex items-center gap-2 text-xs">
        <AccountBadge acc={acc} />
        <span className="text-amber-500/80 font-medium text-xs">Needs sign-in</span>
      </span>
      {canReauth(acc.provider) && (
        <button
          type="button"
          disabled={busy}
          onClick={() => onReauth(acc.account)}
          className="flex items-center gap-1.5 rounded-md bg-amber-500/10 hover:bg-amber-500/20 text-amber-500 px-2.5 py-1 text-xs font-medium transition-colors disabled:opacity-50"
        >
          <LogIn className="h-3 w-3" aria-hidden="true" />
          Re-auth
        </button>
      )}
    </div>
  )
}

export function FleetQuotaPanel() {
  const { accounts, needsAuth, loading, error, refetch } = useAccounts()
  const [reauthBusy, setReauthBusy] = useState(false)

  const handleReauth = useCallback(
    async (accountId: string) => {
      setReauthBusy(true)
      try {
        await invoke('account_reauth', { accountId })
      } finally {
        setReauthBusy(false)
        void refetch()
      }
    },
    [refetch]
  )

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
    <div className="rounded-xl border border-border/60 bg-card/50 backdrop-blur-sm p-5 flex flex-col gap-4 shadow-md">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-bold tracking-tight text-foreground">Fleet Quota</h2>
        <button
          type="button"
          onClick={() => void refetch()}
          className="text-muted-foreground hover:text-foreground transition-colors p-1 hover:bg-muted rounded-md"
          aria-label="Refresh fleet quota"
        >
          <RefreshCw className={['h-3.5 w-3.5', loading ? 'animate-spin' : ''].join(' ')} aria-hidden="true" />
        </button>
      </div>

      {loading && accounts.length === 0 && needsAuth.length === 0 && (
        <p className="text-xs text-muted-foreground animate-pulse">Loading…</p>
      )}
      {error && <p className="text-xs text-destructive">{error}</p>}

      {!loading && !error && accounts.length === 0 && needsAuth.length === 0 && (
        <p className="text-xs text-muted-foreground">No accounts configured.</p>
      )}

      {needsAuth.map((acc) => (
        <NeedsAuthRow key={acc.account} acc={acc} busy={reauthBusy} onReauth={handleReauth} />
      ))}

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
