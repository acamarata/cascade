/**
 * Purpose: Three stat chips showing period-wide usage totals from UsageSummary.
 * Inputs: summary — UsageSummary from UsageHistoryResponse; days — selected range.
 * Outputs: Row of three chips: total tokens, total cost USD, average daily tokens.
 * Constraints: Pure presentational; no data fetching. Styled like fleet-quota stat chips.
 *   Handles zero values gracefully (shows 0 / $0.00 / 0).
 * SPORT: T-P3-E02-24 UsageSummaryRow
 */
import type { UsageSummary } from '@/lib/api'

interface UsageSummaryRowProps {
  summary: UsageSummary
  days: number
}

/** Format a raw token count as a compact string: 1234 → "1.2K", 1234567 → "1.2M". */
function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

/** Stat chip with a label and a prominent value. */
function StatChip({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-md border border-border bg-muted/40 px-3 py-2 min-w-[120px]">
      <span className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</span>
      <span className="text-sm font-semibold text-foreground tabular-nums">{value}</span>
    </div>
  )
}

export function UsageSummaryRow({ summary, days }: UsageSummaryRowProps) {
  const totalTokens = summary.total_tokens_in + summary.total_tokens_out
  const avgDaily = days > 0 ? Math.round(totalTokens / days) : 0

  return (
    <div
      className="flex flex-wrap gap-2"
      aria-label="Usage summary for selected period"
    >
      <StatChip label="Total tokens" value={formatTokens(totalTokens)} />
      <StatChip label="Total cost" value={`$${summary.total_cost_usd.toFixed(2)}`} />
      <StatChip label="Avg daily" value={formatTokens(avgDaily)} />
    </div>
  )
}
