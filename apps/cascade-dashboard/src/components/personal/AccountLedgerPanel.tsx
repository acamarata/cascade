/**
 * Purpose: Historical per-account usage ledger — tokens in/out, cost, period — from
 *   /api/personal/account-ledger, with a total-cost summary.
 * Inputs: none — fetches via usePersonal (30s refresh).
 * Outputs: A sortable-ish table of LedgerEntry rows grouped by account/model/period
 *   plus a total cost footer. Loading / error / empty states handled.
 * Constraints: Read-only. Costs shown to 4 decimals (USD). Pure Tailwind.
 * SPORT: T-P3-E02-13 AccountLedgerPanel
 */
import { usePersonal } from '@/hooks/usePersonal'
import type { AccountLedgerResponse } from '@/lib/api'

/** Format a USD cost to a compact fixed-decimal string. */
function fmtCost(n: number): string {
  return `$${n.toFixed(4)}`
}

/** Format a token count with thousands separators. */
function fmtTokens(n: number): string {
  return n.toLocaleString('en-US')
}

export function AccountLedgerPanel() {
  const { data, loading, error } = usePersonal<AccountLedgerResponse>('/api/personal/account-ledger')

  if (loading) {
    return <div className="text-muted-foreground text-sm p-4">Loading account ledger…</div>
  }
  if (error) {
    return <div className="text-destructive text-sm p-4">Failed to load ledger: {error}</div>
  }
  if (!data || data.entries.length === 0) {
    return <div className="text-muted-foreground text-sm p-4">No usage recorded yet.</div>
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="overflow-x-auto rounded-lg border border-border">
        <table className="w-full text-sm">
          <thead className="bg-muted/50 text-muted-foreground">
            <tr>
              <th className="px-3 py-2 text-left font-medium">Account</th>
              <th className="px-3 py-2 text-left font-medium">Model</th>
              <th className="px-3 py-2 text-left font-medium">Period</th>
              <th className="px-3 py-2 text-right font-medium">Tokens In</th>
              <th className="px-3 py-2 text-right font-medium">Tokens Out</th>
              <th className="px-3 py-2 text-right font-medium">Cost</th>
            </tr>
          </thead>
          <tbody>
            {data.entries.map((e, i) => (
              <tr key={`${e.account}-${e.model}-${e.period}-${i}`} className="border-t border-border">
                <td className="px-3 py-2 text-foreground">{e.account}</td>
                <td className="px-3 py-2 text-muted-foreground">{e.model}</td>
                <td className="px-3 py-2 text-muted-foreground">{e.period}</td>
                <td className="px-3 py-2 text-right tabular-nums">{fmtTokens(e.tokens_in)}</td>
                <td className="px-3 py-2 text-right tabular-nums">{fmtTokens(e.tokens_out)}</td>
                <td className="px-3 py-2 text-right tabular-nums">{fmtCost(e.cost)}</td>
              </tr>
            ))}
          </tbody>
          <tfoot className="border-t-2 border-border bg-muted/30">
            <tr>
              <td className="px-3 py-2 font-medium text-foreground" colSpan={5}>
                Total
              </td>
              <td className="px-3 py-2 text-right font-semibold tabular-nums text-foreground">
                {fmtCost(data.total_cost)}
              </td>
            </tr>
          </tfoot>
        </table>
      </div>
    </div>
  )
}
