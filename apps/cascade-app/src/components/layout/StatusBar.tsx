/**
 * Purpose: Bottom status bar — shows daemon connection state, last ping timestamp,
 *   MCP port when connected, and a compact Fleet chip that toggles a floating
 *   FleetWidget panel above the bar.
 * Inputs:  useDaemonStatus (daemon RPC ping), useAccounts (quota.json snapshot +
 *   freshness, used for both the Fleet dot color and the "Daemon" label).
 * Outputs: <footer role="contentinfo"> landmark at the bottom of the app chrome.
 * Constraints:
 *   - role="contentinfo" for accessibility. Height h-6.
 *   - Fleet chip: colored dot (green/yellow/red) = worst utilization across
 *     authenticated accounts. `utilization` in quota.json is already 0-100 —
 *     never multiply it by 100 (that was the bug that clamped small
 *     utilizations like 8% to a false 100%).
 *   - The "Daemon" label prefers quota.json freshness (`useAccounts().isLive`)
 *     over the raw JSON-RPC ping: `accounts_list` reads quota.json directly
 *     and has no dependency on the RPC health-check, so a failed ping must
 *     never show "Disconnected"/"Error" while the store is actually fresh.
 *   - Fleet panel: absolute-positioned above the bar, z-50, toggled by useState.
 *   - No Radix Popover needed — simple absolute positioning.
 * SPORT: MASTER-COMPONENTS.md — StatusBar
 */

import { useState } from 'react'
import { BarChart2 } from 'lucide-react'
import { useDaemonStatus } from '../../store/index'
import { useAccounts } from '../../features/accounts/useAccounts'
import { FleetWidget } from '../widget/FleetWidget'
import type { AccountQuota } from '../../features/accounts/types'

/**
 * Determine the worst-case utilization dot color across all accounts.
 * `utilization` is already a 0-100 percentage — red >80, yellow >=50, green otherwise.
 */
function worstUtilColor(accounts: AccountQuota[]): string {
  let worst = 0
  for (const acc of accounts) {
    const util = acc.usage?.five_hour?.utilization
    if (util != null) worst = Math.max(worst, util)
  }
  if (worst > 80) return 'bg-red-500'
  if (worst >= 50) return 'bg-yellow-400'
  return 'bg-green-500'
}

export function StatusBar() {
  const { status, lastPing, data } = useDaemonStatus()
  const { accounts, isLive, lastUpdated } = useAccounts()
  const [fleetOpen, setFleetOpen] = useState(false)

  // Quota-store freshness wins over the raw daemon ping — the Fleet numbers
  // are read straight from quota.json and are correct even when the RPC
  // health check is erroring out.
  const effectiveStatus = isLive ? 'connected' : status

  const pingText =
    isLive && lastUpdated
      ? `Quota data: live (updated ${new Date(lastUpdated).toLocaleTimeString()})`
      : lastPing
        ? `Last ping: ${new Date(lastPing).toLocaleTimeString()}`
        : 'No ping yet'

  const mcpText =
    status === 'connected' && data && 'mcpPort' in data && data.mcpPort
      ? `MCP :${data.mcpPort}`
      : null

  const dotColor = worstUtilColor(accounts)

  return (
    <div className="relative">
      {/* ── Fleet floating panel ── */}
      {fleetOpen && (
        <div
          className="absolute bottom-7 right-0 z-50 w-80 rounded-lg border border-border bg-background p-4 shadow-lg"
          role="dialog"
          aria-label="Fleet quota panel"
        >
          <FleetWidget />
        </div>
      )}

      <footer
        role="contentinfo"
        className="flex h-6 shrink-0 items-center gap-4 border-t border-border bg-muted/40 px-4 text-xs text-muted-foreground"
      >
        <span>
          Daemon: <span className="capitalize">{effectiveStatus}</span>
        </span>

        <span aria-label="Quota data freshness">{pingText}</span>

        {mcpText && <span aria-label="MCP server port">{mcpText}</span>}

        {/* Spacer */}
        <span className="ml-auto" />

        {/* Fleet chip */}
        <button
          type="button"
          onClick={() => setFleetOpen((v) => !v)}
          aria-pressed={fleetOpen}
          aria-label="Toggle fleet quota panel"
          className="flex items-center gap-1 rounded px-1.5 py-0.5 hover:bg-accent/50 transition-colors text-muted-foreground hover:text-foreground"
        >
          <span
            className={['inline-block h-1.5 w-1.5 rounded-full', dotColor].join(' ')}
            aria-hidden="true"
          />
          <BarChart2 className="h-3 w-3" aria-hidden="true" />
          <span>Fleet</span>
        </button>
      </footer>
    </div>
  )
}
