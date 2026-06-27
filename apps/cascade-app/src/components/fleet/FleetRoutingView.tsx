/**
 * Purpose: Routing view — table of all fleet accounts with provider, 5h%, 7d%,
 *   status, and reset time. Helps decide which account to route work to.
 * Inputs:  useAccounts() IPC (30s poll).
 * Outputs: A table with columns: Label | Provider | 5h% | 7d% | Status | Reset.
 * Constraints:
 *   - Read-only display; no actions.
 *   - Routing event stream deferred to fleet-01.
 * SPORT: MASTER-COMPONENTS.md — FleetRoutingView (fleet-02)
 */

// TODO(fleet-01-events): replace with live RoutingEvent stream once fleet-01 lands

import { useAccounts } from '../../features/accounts/useAccounts'
import { accountLabel, pct, statusColor } from '../../features/accounts/types'
import type { AccountQuota } from '../../features/accounts/types'

function formatReset(acc: AccountQuota): string {
  const ts = acc.usage?.five_hour?.resets_at
  if (!ts) return '—'
  const d = new Date(ts * 1000)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function statusBadge(status: string | null | undefined): string {
  if (!status) return '—'
  return status.replace(/_/g, ' ')
}

export function FleetRoutingView() {
  const { accounts, loading, error } = useAccounts()

  return (
    <div className="flex flex-col gap-3">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Routing
      </h3>

      {loading && accounts.length === 0 && (
        <p className="text-xs text-muted-foreground animate-pulse">Loading…</p>
      )}
      {error && <p className="text-xs text-destructive">{error}</p>}

      {!loading && !error && accounts.length === 0 && (
        <p className="text-xs text-muted-foreground">No accounts configured.</p>
      )}

      {accounts.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-xs border-collapse">
            <thead>
              <tr className="border-b border-border text-muted-foreground">
                <th className="py-1.5 pr-3 text-left font-medium">Label</th>
                <th className="py-1.5 pr-3 text-left font-medium">Provider</th>
                <th className="py-1.5 pr-3 text-right font-medium">5h%</th>
                <th className="py-1.5 pr-3 text-right font-medium">7d%</th>
                <th className="py-1.5 pr-3 text-left font-medium">Status</th>
                <th className="py-1.5 text-right font-medium">Reset</th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((acc) => {
                const label = accountLabel(acc)
                const fhPct = pct(
                  acc.usage?.five_hour?.utilization != null
                    ? acc.usage.five_hour.utilization * 100
                    : null,
                )
                const sdPct = pct(
                  acc.usage?.seven_day?.utilization != null
                    ? acc.usage.seven_day.utilization * 100
                    : null,
                )
                const statusClasses = statusColor(acc.status)

                return (
                  <tr key={acc.account} className="border-b border-border/50 last:border-0">
                    <td className="py-1.5 pr-3 font-mono font-medium text-foreground">{label}</td>
                    <td className="py-1.5 pr-3 text-muted-foreground capitalize">{acc.provider}</td>
                    <td className="py-1.5 pr-3 text-right tabular-nums text-foreground">{fhPct}</td>
                    <td className="py-1.5 pr-3 text-right tabular-nums text-foreground">{sdPct}</td>
                    <td className="py-1.5 pr-3">
                      <span
                        className={[
                          'inline-block rounded-full px-1.5 py-0.5 text-[10px] font-medium capitalize',
                          statusClasses,
                        ].join(' ')}
                      >
                        {statusBadge(acc.status)}
                      </span>
                    </td>
                    <td className="py-1.5 text-right tabular-nums text-muted-foreground">
                      {formatReset(acc)}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
