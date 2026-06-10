/**
 * Purpose: Top section of the Usage page — period selector, 3 metric cards
 *   (Total Tokens, Total Cost, Active Providers), and a daily token bar chart.
 * Inputs:  summary — UsageSummary | null; history — MonthlyUsage[]; period/onPeriodChange
 * Outputs: Panel with metric cards + a Recharts BarChart of daily tokens.
 * Constraints:
 *   - Uses recharts BarChart (horizontal bars, responsive).
 *   - history prop drives the mini trend chart (monthly totals).
 *   - Shows skeleton placeholders while loading.
 *   - Never throws on null/empty data.
 * SPORT: MASTER-COMPONENTS.md — SummaryPanel (usage)
 */

import React from 'react'
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts'
import { RefreshCw } from 'lucide-react'
import type { UsageSummary, MonthlyUsage, UsagePeriod } from '../../types/ipc'
import { PeriodSelector } from './PeriodSelector'

interface MetricCardProps {
  label: string
  value: string
}

function MetricCard({ label, value }: MetricCardProps): React.ReactElement {
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums">{value}</p>
    </div>
  )
}

interface SummaryPanelProps {
  summary: UsageSummary | null
  history: MonthlyUsage[]
  period: UsagePeriod
  onPeriodChange: (p: UsagePeriod) => void
  loading: boolean
  onRefresh: () => void
}

/** Format a token count with locale separators. */
function fmtTokens(n: number): string {
  return n.toLocaleString()
}

/** Format USD cost to 2 decimal places. */
function fmtCost(usd: number): string {
  return `$${usd.toFixed(2)}`
}

/**
 * Summary panel: period picker, three metric cards, monthly trend bar chart.
 */
export function SummaryPanel({
  summary,
  history,
  period,
  onPeriodChange,
  loading,
  onRefresh,
}: SummaryPanelProps): React.ReactElement {
  const totalTokens = summary?.totalTokens ?? 0
  const totalCost = summary?.totalCostUsd ?? 0
  const activeProviders = summary ? summary.byAccount.length : 0
  const periodLabel = summary?.periodLabel ?? '—'

  // Build chart data from monthly history.
  const chartData = history.map((m) => ({
    month: m.monthLabel,
    tokens: m.tokens,
    cost: m.costUsd,
  }))

  return (
    <section aria-labelledby="summary-heading" className="space-y-4">
      {/* Header row */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 id="summary-heading" className="text-lg font-semibold">
            Usage Summary
          </h2>
          {periodLabel && (
            <p className="text-sm text-muted-foreground">{periodLabel}</p>
          )}
        </div>
        <div className="flex items-center gap-3">
          <PeriodSelector period={period} onChange={onPeriodChange} />
          <button
            type="button"
            onClick={onRefresh}
            aria-label="Refresh usage data"
            className="flex h-8 w-8 items-center justify-center rounded-md border border-border text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
          >
            <RefreshCw className={['h-4 w-4', loading ? 'animate-spin' : ''].join(' ')} aria-hidden="true" />
          </button>
        </div>
      </div>

      {/* Metric cards */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <MetricCard label="Total Tokens" value={fmtTokens(totalTokens)} />
        <MetricCard label="Total Cost" value={fmtCost(totalCost)} />
        <MetricCard label="Active Accounts" value={String(activeProviders)} />
      </div>

      {/* Monthly trend chart */}
      {chartData.length > 0 && (
        <div className="rounded-lg border border-border bg-card p-4">
          <p className="mb-3 text-xs font-medium uppercase tracking-wide text-muted-foreground">
            Monthly Token Trend
          </p>
          <ResponsiveContainer width="100%" height={160}>
            <BarChart data={chartData} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" />
              <XAxis
                dataKey="month"
                tick={{ fontSize: 11, fill: 'hsl(var(--muted-foreground))' }}
                axisLine={false}
                tickLine={false}
              />
              <YAxis
                tick={{ fontSize: 11, fill: 'hsl(var(--muted-foreground))' }}
                axisLine={false}
                tickLine={false}
                tickFormatter={(v: number) => (v >= 1000 ? `${(v / 1000).toFixed(0)}k` : String(v))}
              />
              <Tooltip
                contentStyle={{
                  background: 'hsl(var(--popover))',
                  border: '1px solid hsl(var(--border))',
                  borderRadius: 6,
                  fontSize: 12,
                }}
                // eslint-disable-next-line @typescript-eslint/no-explicit-any
                formatter={(value: any, name: any) =>
                  name === 'tokens' ? [fmtTokens(Number(value) || 0), 'Tokens'] : [fmtCost(Number(value) || 0), 'Cost']
                }
              />
              <Bar dataKey="tokens" fill="hsl(var(--primary))" radius={[3, 3, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}
    </section>
  )
}
