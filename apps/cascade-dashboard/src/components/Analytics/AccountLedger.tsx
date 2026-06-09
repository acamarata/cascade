/**
 * Purpose: Per-account historical ledger drilldown — shows a small BarChart of daily
 *   token totals for a single account alongside a date-sorted table of daily rows.
 * Inputs: account — AccountUsage (filtered by caller to a single account_id); onBack — callback to
 *   return to the summary view.
 * Outputs: Two-column layout: left = per-account Recharts BarChart; right = <table> with
 *   columns Date | Tokens In | Tokens Out | Cost USD, sorted oldest-to-newest.
 *   Alternating row shading via Tailwind even:bg-gray-50 dark:even:bg-gray-700.
 *   "Back to summary" button at top.
 * Constraints: No fetch calls — data comes from the caller (client-side filtering).
 *   Empty totals_by_day renders a "no data" message. Table has <thead> with accessible
 *   column headers.
 * SPORT: T-P3-E02-25 AccountLedger
 */
import { format, parseISO } from 'date-fns'
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  Cell,
} from 'recharts'
import { cn } from '@/lib/utils'
import type { AccountUsage, DailyTotal } from '@/lib/api'

// ── helpers ───────────────────────────────────────────────────────────────────

function formatDateLabel(isoDate: string): string {
  try {
    return format(parseISO(isoDate), 'MMM d')
  } catch {
    return isoDate
  }
}

function formatYTick(value: number): string {
  if (value === 0) return '0'
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return String(value)
}

// ── custom tooltip ────────────────────────────────────────────────────────────

interface TooltipProps {
  active?: boolean
  payload?: Array<{ payload: DailyTotal & { label: string } }>
}

function LedgerTooltip({ active, payload }: TooltipProps) {
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

// ── props ─────────────────────────────────────────────────────────────────────

export interface AccountLedgerProps {
  account: AccountUsage
  onBack: () => void
}

// ── component ─────────────────────────────────────────────────────────────────

export function AccountLedger({ account, onBack }: AccountLedgerProps) {
  // Sort oldest-to-newest for both chart and table
  const rows = [...account.totals_by_day].sort((a, b) =>
    a.date.localeCompare(b.date),
  )

  const chartData = rows.map((d) => ({
    ...d,
    label: formatDateLabel(d.date),
    tokens: d.tokens_in + d.tokens_out,
  }))

  const hasData = rows.length > 0

  return (
    <div className="flex flex-col gap-4">
      {/* Back navigation */}
      <button
        onClick={onBack}
        className="self-start flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
        aria-label="Back to summary view"
      >
        ← Back to summary
      </button>

      {/* Account heading */}
      <h3 className="text-sm font-semibold text-foreground">
        {account.label}
        <span className="ml-2 text-xs font-normal text-muted-foreground">
          ({account.account_id})
        </span>
      </h3>

      {!hasData && (
        <p className="text-xs text-muted-foreground">No usage data for this account in the selected period.</p>
      )}

      {hasData && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {/* Left: per-account bar chart */}
          <div aria-label={`Daily token usage chart for ${account.label}`}>
            <ResponsiveContainer width="100%" height={200}>
              <BarChart
                data={chartData}
                margin={{ top: 4, right: 8, left: 0, bottom: 0 }}
                aria-label={`Token usage history for ${account.label}`}
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
                <Tooltip content={<LedgerTooltip />} cursor={{ fill: 'var(--color-muted, rgba(0,0,0,0.05))' }} />
                <Bar
                  dataKey="tokens"
                  radius={[2, 2, 0, 0]}
                  maxBarSize={28}
                  aria-label="Daily token total for account"
                >
                  {chartData.map((_, index) => (
                    <Cell
                      key={`acct-cell-${index}`}
                      fill="hsl(var(--primary, 221 83% 53%))"
                    />
                  ))}
                </Bar>
              </BarChart>
            </ResponsiveContainer>
          </div>

          {/* Right: daily ledger table */}
          <div className="overflow-auto max-h-[240px]">
            <table className="w-full text-xs border-collapse" aria-label={`Daily usage ledger for ${account.label}`}>
              <thead>
                <tr className="text-left text-muted-foreground border-b border-border">
                  <th scope="col" className="py-1.5 pr-3 font-medium">Date</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium text-right">Tokens In</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium text-right">Tokens Out</th>
                  <th scope="col" className="py-1.5 font-medium text-right">Cost (USD)</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row, i) => (
                  <tr
                    key={row.date}
                    className={cn(
                      'border-b border-border/50',
                      i % 2 === 0
                        ? 'bg-transparent'
                        : 'bg-gray-50 dark:bg-gray-700/40',
                    )}
                  >
                    <td className="py-1 pr-3 text-foreground">{formatDateLabel(row.date)}</td>
                    <td className="py-1 pr-3 text-right text-muted-foreground">{row.tokens_in.toLocaleString()}</td>
                    <td className="py-1 pr-3 text-right text-muted-foreground">{row.tokens_out.toLocaleString()}</td>
                    <td className="py-1 text-right text-muted-foreground">${row.cost_usd.toFixed(4)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}
