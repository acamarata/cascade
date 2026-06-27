/**
 * Purpose: Fleet sidebar widget — per-model quota gauges + per-account cost rows,
 *   fed by the central usage store via the useUsageData hook (T-P3-E04-30).
 * Inputs:  useUsageData hook (60 s auto-refresh, period = current week)
 * Outputs: Two sections appended below existing availability indicators:
 *   1. "Quota This Week" — one QuotaGaugeRow per model
 *   2. "Account Costs"  — one AccountCostRow per account
 * Constraints:
 *   - Zero layout changes to any content above the quota sections.
 *   - Graceful empty/loading states; never throws on missing data.
 *   - 60 s auto-refresh is inherited from useUsageData.
 *   - System tray standalone widget variant is deferred to P4.
 * SPORT: MASTER-COMPONENTS.md — FleetWidget
 */

import React from 'react'
import { BarChart2, RefreshCw } from 'lucide-react'
import { QuotaGaugeRow } from './QuotaGaugeRow'
import { AccountCostRow } from './AccountCostRow'
import { useUsageData } from '../../hooks/useUsageData'
import { useFleetQuotaConfig } from '../../hooks/useFleetQuotaConfig'

export function FleetWidget(): React.ReactElement {
  const { summary, loading, error, refetch } = useUsageData({ kind: 'week', offset: 0 })
  const { estimates } = useFleetQuotaConfig()

  return (
    <div className="space-y-4 text-sm">
      {/* ── Quota This Week ── */}
      <section aria-labelledby="quota-section-heading">
        <div className="flex items-center justify-between mb-1">
          <h3
            id="quota-section-heading"
            className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground"
          >
            <BarChart2 className="h-3.5 w-3.5" aria-hidden="true" />
            Quota This Week
          </h3>
          <button
            type="button"
            onClick={refetch}
            aria-label="Refresh quota data"
            className="text-muted-foreground hover:text-foreground transition-colors"
          >
            <RefreshCw
              className={['h-3 w-3', loading ? 'animate-spin' : ''].join(' ')}
              aria-hidden="true"
            />
          </button>
        </div>

        {error && (
          <p className="text-xs text-muted-foreground italic py-1">
            Quota data unavailable (daemon offline)
          </p>
        )}

        {!error && summary === null && !loading && (
          <p className="text-xs text-muted-foreground italic py-1">No usage data yet.</p>
        )}

        {loading && summary === null && (
          <p className="text-xs text-muted-foreground animate-pulse py-1">Loading…</p>
        )}

        {summary !== null && summary.byModel.length === 0 && (
          <p className="text-xs text-muted-foreground italic py-1">No model usage this week.</p>
        )}

        {summary !== null &&
          summary.byModel.map((row) => (
            <QuotaGaugeRow
              key={row.model}
              model={row.model}
              tokensUsed={row.tokens}
              costUsd={row.costUsd}
              estimates={estimates}
            />
          ))}
      </section>

      {/* ── Account Costs ── */}
      {summary !== null && summary.byAccount.length > 0 && (
        <section aria-labelledby="accounts-section-heading">
          <h3
            id="accounts-section-heading"
            className="text-xs font-semibold uppercase tracking-wide text-muted-foreground mb-1"
          >
            Account Costs
          </h3>
          {summary.byAccount.map((acct) => (
            <AccountCostRow
              key={acct.email}
              email={acct.email}
              costUsd={acct.costUsd}
              lastActive=""
            />
          ))}
        </section>
      )}
    </div>
  )
}
