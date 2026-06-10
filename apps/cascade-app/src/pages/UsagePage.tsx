/**
 * Purpose: Usage analytics page — composes SummaryPanel, ModelBreakdownChart,
 *   and AccountLedger with shared state (period, selectedAccount).
 * Inputs:  None (self-contained; reads IPC via useUsageData hook).
 * Outputs: Three-panel usage page at /usage route.
 * Constraints:
 *   - Auto-refresh every 60 s (inherited from useUsageData).
 *   - Empty state when daemon has no usage data (total_tokens === 0 + empty history).
 *   - Period state drives both SummaryPanel and AccountLedger IPC calls.
 *   - Selected account drives ledger fetch.
 * SPORT: MASTER-COMPONENTS.md — UsagePage
 */

import React, { useState } from 'react'
import { BarChart2 } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import type { UsagePeriod } from '../types/ipc'
import { useUsageData } from '../hooks/useUsageData'
import { SummaryPanel } from '../components/usage/SummaryPanel'
import { ModelBreakdownChart } from '../components/usage/ModelBreakdownChart'
import { AccountLedger } from '../components/usage/AccountLedger'

const DEFAULT_PERIOD: UsagePeriod = { kind: 'week', offset: 0 }

/**
 * Empty state shown when the daemon has no usage files yet.
 */
function EmptyState(): React.ReactElement {
  const navigate = useNavigate()
  return (
    <div className="flex flex-col items-center justify-center py-20 text-center">
      <BarChart2 className="mb-4 h-12 w-12 text-muted-foreground/40" aria-hidden="true" />
      <h2 className="text-lg font-semibold">No usage data yet</h2>
      <p className="mt-1 max-w-sm text-sm text-muted-foreground">
        Usage data will appear here once you start using cascade with AI providers.
      </p>
      <button
        type="button"
        onClick={() => navigate('/settings/providers')}
        className="mt-4 rounded-md border border-border px-4 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
      >
        Go to Provider Settings
      </button>
    </div>
  )
}

/**
 * Full usage analytics page: summary + model breakdown + per-account ledger.
 */
export function UsagePage(): React.ReactElement {
  const [period, setPeriod] = useState<UsagePeriod>(DEFAULT_PERIOD)
  const [selectedAccount, setSelectedAccount] = useState('')

  const { summary, history, ledger, loading, error, refetch } = useUsageData(
    period,
    selectedAccount
  )

  const isEmpty =
    !loading && !error && (summary === null || (summary.totalTokens === 0 && history.length === 0))

  return (
    <main className="flex-1 p-6 space-y-6">
      {/* Page heading */}
      <div className="flex items-center gap-2">
        <BarChart2 className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-2xl font-semibold">Usage</h1>
      </div>

      {/* Error banner */}
      {error && (
        <div
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive"
        >
          {error.includes('daemon') || error.includes('connect')
            ? 'Cascade daemon is not running. Start it to see usage data.'
            : error}
        </div>
      )}

      {/* Empty state */}
      {isEmpty && <EmptyState />}

      {/* Data panels — show when not empty or still loading */}
      {!isEmpty && (
        <>
          {/* Panel 1 — Summary + mini chart */}
          <SummaryPanel
            summary={summary}
            history={history}
            period={period}
            onPeriodChange={(p) => {
              setPeriod(p)
              setSelectedAccount('')
            }}
            loading={loading}
            onRefresh={refetch}
          />

          {/* Panel 2 — By-model horizontal bar chart */}
          <ModelBreakdownChart byModel={summary?.byModel ?? []} loading={loading} />

          {/* Panel 3 — Per-account collapsible ledger */}
          <AccountLedger
            accounts={summary?.byAccount ?? []}
            ledger={ledger}
            selectedAccount={selectedAccount}
            onAccountChange={setSelectedAccount}
            loading={loading}
          />
        </>
      )}
    </main>
  )
}
