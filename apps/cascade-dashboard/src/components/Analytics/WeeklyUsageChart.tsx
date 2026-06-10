/**
 * Purpose: Analytics panel — date-range picker + Recharts bar chart of daily token usage
 *   + summary row + per-account row list with drilldown into AccountLedger.
 * Inputs: none — self-contained with internal date-range state.
 * Outputs: Date picker (7/14/30/90 day presets + two date inputs) above a BarChart
 *   (one bar per day, X-axis date label, Y-axis token count), a UsageSummaryRow, and
 *   a clickable account row list below. Clicking an account renders AccountLedger drilldown.
 * Constraints: Data fetching isolated in useUsageHistory; no fetch calls in JSX.
 *   Loading → skeleton; error → alert; empty data → chart with zeroed axis.
 *   Y-axis formatter handles 0 and values >1M without overflow (uses compact notation).
 *   Bars have aria-label; date inputs have associated labels.
 *   selectedAccount resets to null when date range changes (days changes).
 * SPORT: T-P3-E02-24 WeeklyUsageChart | T-P3-E02-25 AccountLedger integration
 */
import { useState, useCallback, useEffect } from 'react'
import { format, subDays, parseISO } from 'date-fns'
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  Cell,
} from 'recharts'
import { useUsageHistory } from '@/hooks/useUsageHistory'
import { UsageSummaryRow } from './UsageSummaryRow'
import { AccountLedger } from './AccountLedger'
import type { AccountUsage, DailyTotal } from '@/lib/api'

// ── helpers ───────────────────────────────────────────────────────────────────

/** Format token count for Y-axis tick labels: 0→"0", 1200→"1.2K", 1.5M→"1.5M". */
function formatYTick(value: number): string {
  if (value === 0) return '0'
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return String(value)
}

/** Format ISO date "YYYY-MM-DD" to "MMM DD" for bar X-axis label. */
function formatDateLabel(isoDate: string): string {
  try {
    return format(parseISO(isoDate), 'MMM d')
  } catch {
    return isoDate
  }
}

// ── date-range state ──────────────────────────────────────────────────────────

const PRESETS = [7, 14, 30, 90] as const
type Preset = (typeof PRESETS)[number]

function todayStr(): string {
  return format(new Date(), 'yyyy-MM-dd')
}

function daysAgoStr(n: number): string {
  return format(subDays(new Date(), n - 1), 'yyyy-MM-dd')
}

// ── custom tooltip ────────────────────────────────────────────────────────────

interface TooltipProps {
  active?: boolean
  payload?: Array<{ payload: DailyTotal & { label: string } }>
  label?: string
}

function CustomTooltip({ active, payload }: TooltipProps) {
  if (!active || !payload || payload.length === 0) return null
  const d = payload[0].payload
  return (
    <div className="rounded-md border border-border bg-popover px-3 py-2 text-xs shadow-md">
      <p className="font-medium text-foreground mb-1">{d.label}</p>
      <p className="text-muted-foreground">In: {d.tokens_in.toLocaleString()}</p>
      <p className="text-muted-foreground">Out: {d.tokens_out.toLocaleString()}</p>
      <p className="text-muted-foreground">Cost: ${d.cost_usd.toFixed(4)}</p>
    </div>
  )
}

// ── skeleton ──────────────────────────────────────────────────────────────────

function ChartSkeleton() {
  return (
    <div
      className="h-[240px] rounded-md bg-muted/50 animate-pulse flex items-center justify-center"
      aria-label="Loading usage chart"
      aria-busy="true"
    >
      <span className="text-xs text-muted-foreground">Loading…</span>
    </div>
  )
}

// ── main component ────────────────────────────────────────────────────────────

export function WeeklyUsageChart() {
  const [activePreset, setActivePreset] = useState<Preset>(7)
  const [fromDate, setFromDate] = useState<string>(() => daysAgoStr(7))
  const [toDate, setToDate] = useState<string>(todayStr)
  const [selectedAccount, setSelectedAccount] = useState<AccountUsage | null>(null)

  // days = difference between to/from + 1, clamped to [1, 90]
  const days = Math.min(
    90,
    Math.max(
      1,
      Math.round(
        (new Date(toDate).getTime() - new Date(fromDate).getTime()) /
          (1000 * 60 * 60 * 24),
      ) + 1,
    ),
  )

  const { data, loading, error, refetch } = useUsageHistory(days)

  // Reset drilldown whenever date range changes (CR-A: selectedAccount resets on days change)
  useEffect(() => {
    setSelectedAccount(null)
  }, [days])

  // Preset selection resets both date inputs
  const handlePreset = useCallback((p: Preset) => {
    setActivePreset(p)
    setFromDate(daysAgoStr(p))
    setToDate(todayStr())
  }, [])

  // When user edits date inputs, clear preset highlight
  const handleFromChange = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    setFromDate(e.target.value)
    setActivePreset(0 as Preset)
  }, [])

  const handleToChange = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    setToDate(e.target.value)
    setActivePreset(0 as Preset)
  }, [])

  // Map daily_totals to Recharts data array
  const chartData = (data?.daily_totals ?? []).map((d) => ({
    ...d,
    label: formatDateLabel(d.date),
    tokens: d.tokens_in + d.tokens_out,
  }))

  // Total cost per account for display in the account row list
  const accountRows = data?.per_account ?? []

  return (
    <section
      aria-labelledby="analytics-heading"
      className="rounded-lg border border-border bg-card p-4 flex flex-col gap-4"
    >
      {/* Header */}
      <div className="flex items-center justify-between flex-wrap gap-2">
        <h2
          id="analytics-heading"
          className="text-sm font-semibold text-foreground"
        >
          Analytics
        </h2>
        <button
          onClick={() => refetch()}
          className="text-xs text-muted-foreground hover:text-foreground transition-colors"
          aria-label="Refresh usage history"
        >
          ↻
        </button>
      </div>

      {/* Date-range controls */}
      <div className="flex flex-wrap items-center gap-2">
        {/* Preset buttons */}
        {PRESETS.map((p) => (
          <button
            key={p}
            onClick={() => handlePreset(p)}
            className={`rounded px-2 py-1 text-xs font-medium transition-colors ${
              activePreset === p
                ? 'bg-primary text-primary-foreground'
                : 'bg-muted text-muted-foreground hover:text-foreground'
            }`}
            aria-pressed={activePreset === p}
            aria-label={`Last ${p} days`}
          >
            {p}d
          </button>
        ))}

        {/* Custom date inputs */}
        <div className="flex items-center gap-1.5 ml-2">
          <label htmlFor="usage-from" className="text-xs text-muted-foreground sr-only">
            From date
          </label>
          <input
            id="usage-from"
            type="date"
            value={fromDate}
            max={toDate}
            onChange={handleFromChange}
            className="rounded border border-border bg-background px-2 py-1 text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
            aria-label="From date"
          />
          <span className="text-xs text-muted-foreground">–</span>
          <label htmlFor="usage-to" className="text-xs text-muted-foreground sr-only">
            To date
          </label>
          <input
            id="usage-to"
            type="date"
            value={toDate}
            max={todayStr()}
            onChange={handleToChange}
            className="rounded border border-border bg-background px-2 py-1 text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
            aria-label="To date"
          />
        </div>

        {data && (
          <span className="text-xs text-muted-foreground ml-auto">
            {days} day{days !== 1 ? 's' : ''}
          </span>
        )}
      </div>

      {/* Chart area */}
      {loading && <ChartSkeleton />}

      {error && (
        <div
          role="alert"
          className="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          Failed to load usage history: {error}
        </div>
      )}

      {!loading && !error && (
        <div aria-label={`Bar chart of daily token usage over ${days} days`}>
          <ResponsiveContainer width="100%" height={240}>
            <BarChart
              data={chartData}
              margin={{ top: 4, right: 8, left: 0, bottom: 0 }}
              aria-label={`Daily token usage chart — ${days} days`}
            >
              <XAxis
                dataKey="label"
                tick={{ fontSize: 10, fill: 'var(--color-muted-foreground, #6b7280)' }}
                tickLine={false}
                axisLine={false}
                interval="preserveStartEnd"
              />
              <YAxis
                tickFormatter={formatYTick}
                tick={{ fontSize: 10, fill: 'var(--color-muted-foreground, #6b7280)' }}
                tickLine={false}
                axisLine={false}
                width={40}
              />
              <Tooltip content={<CustomTooltip />} cursor={{ fill: 'var(--color-muted, rgba(0,0,0,0.05))' }} />
              <Bar
                dataKey="tokens"
                radius={[2, 2, 0, 0]}
                maxBarSize={32}
                aria-label="Daily token total"
              >
                {chartData.map((entry, index) => (
                  <Cell
                    key={`cell-${index}`}
                    fill="hsl(var(--primary, 221 83% 53%))"
                    aria-label={`${entry.label}: ${entry.tokens.toLocaleString()} tokens`}
                  />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>

          {chartData.length === 0 && (
            <p className="text-center text-xs text-muted-foreground -mt-10 pb-6">
              No usage data for this period.
            </p>
          )}
        </div>
      )}

      {/* Summary row */}
      {!loading && !error && data && (
        <UsageSummaryRow summary={data.summary} days={days} />
      )}

      {/* Per-account drilldown: account row list or expanded AccountLedger */}
      {!loading && !error && data && (
        <>
          {selectedAccount ? (
            <AccountLedger
              account={selectedAccount}
              onBack={() => setSelectedAccount(null)}
            />
          ) : (
            accountRows.length > 0 && (
              <div className="flex flex-col gap-1">
                <h3 className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
                  Accounts
                </h3>
                <ul role="list" className="flex flex-col gap-0.5">
                  {accountRows.map((acct) => {
                    const totalCost = acct.totals_by_day.reduce(
                      (sum, d) => sum + d.cost_usd,
                      0,
                    )
                    return (
                      <li key={acct.account_id}>
                        <button
                          onClick={() => setSelectedAccount(acct)}
                          className="w-full flex items-center justify-between rounded px-2 py-1.5 text-xs hover:bg-muted/60 transition-colors text-left"
                          aria-label={`View ledger drilldown for ${acct.label}`}
                          title={`Click to view usage history for ${acct.label}`}
                        >
                          <span className="text-foreground truncate">{acct.label}</span>
                          <span className="text-muted-foreground shrink-0 ml-2">
                            ${totalCost.toFixed(4)}
                          </span>
                        </button>
                      </li>
                    )
                  })}
                </ul>
              </div>
            )
          )}
        </>
      )}
    </section>
  )
}
