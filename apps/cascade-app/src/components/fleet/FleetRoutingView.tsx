/**
 * Purpose: Routing view — two sections:
 *   1. Account quota table (Label | Provider | 5h% | 7d% | Status | Reset).
 *   2. Live routing-decision stream (Task | Account | Reason), polled from
 *      GET /api/fleet/routing every 5 s.
 * Inputs:  useAccounts() IPC (4 s poll); /api/fleet/routing HTTP (5 s poll).
 * Outputs: Quota table + routing-event table.
 * Constraints:
 *   - Read-only display; no actions.
 *   - Routing stream section degrades gracefully when daemon is down or ring is empty.
 * SPORT: MASTER-COMPONENTS.md — FleetRoutingView (fleet-01)
 */

import { useEffect, useState } from 'react'
import { useAccounts } from '../../features/accounts/useAccounts'
import { accountLabel, pct, statusColor } from '../../features/accounts/types'
import type { AccountQuota } from '../../features/accounts/types'

// ── Types ─────────────────────────────────────────────────────────────────────

interface RoutingEvent {
  task_class: string
  account_id: string
  model: string
  reason: string
}

// ── Quota helpers ─────────────────────────────────────────────────────────────

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

// ── Routing-event poller hook ─────────────────────────────────────────────────

const ROUTING_POLL_MS = 5_000

/**
 * Poll GET /api/fleet/routing every ROUTING_POLL_MS.
 *
 * Returns the most-recent routing events (most-recent first) and a boolean
 * indicating whether the daemon endpoint is reachable. Silently degrades
 * (empty array, reachable=false) when the daemon is down or returns an error.
 */
function useRoutingEvents(): { events: RoutingEvent[]; reachable: boolean } {
  const [events, setEvents] = useState<RoutingEvent[]>([])
  const [reachable, setReachable] = useState(true)

  useEffect(() => {
    let cancelled = false

    async function poll() {
      try {
        const resp = await fetch('http://127.0.0.1:9761/api/fleet/routing')
        if (!resp.ok) {
          if (!cancelled) setReachable(false)
          return
        }
        const data: RoutingEvent[] = await resp.json()
        if (!cancelled) {
          setEvents(data)
          setReachable(true)
        }
      } catch {
        if (!cancelled) setReachable(false)
      }
    }

    // Immediate first poll, then interval.
    void poll()
    const id = setInterval(() => void poll(), ROUTING_POLL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  return { events, reachable }
}

// ── Component ─────────────────────────────────────────────────────────────────

export function FleetRoutingView() {
  const { accounts, loading, error } = useAccounts()
  const { events: routingEvents, reachable } = useRoutingEvents()

  return (
    <div className="flex flex-col gap-6">
      {/* ── Quota table ──────────────────────────────────────────────────────── */}
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
                  // utilization is already a 0-100 percentage — do not re-scale.
                  const fhPct = pct(acc.usage?.five_hour?.utilization ?? null)
                  const sdPct = pct(acc.usage?.seven_day?.utilization ?? null)
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

      {/* ── Live routing-decision stream ──────────────────────────────────────── */}
      <div className="flex flex-col gap-3">
        <div className="flex items-center gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Recent Routing Decisions
          </h3>
          {!reachable && (
            <span className="text-[10px] text-muted-foreground">(daemon unreachable)</span>
          )}
        </div>

        {reachable && routingEvents.length === 0 && (
          <p className="text-xs text-muted-foreground">
            No routing decisions recorded yet. Decisions appear here as tasks are routed.
          </p>
        )}

        {routingEvents.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full text-xs border-collapse">
              <thead>
                <tr className="border-b border-border text-muted-foreground">
                  <th className="py-1.5 pr-3 text-left font-medium">Task</th>
                  <th className="py-1.5 pr-3 text-left font-medium">Account</th>
                  <th className="py-1.5 text-left font-medium">Reason</th>
                </tr>
              </thead>
              <tbody>
                {routingEvents.map((ev, i) => (
                  <tr
                    key={`${ev.task_class}-${ev.account_id}-${i}`}
                    className="border-b border-border/50 last:border-0"
                  >
                    <td className="py-1.5 pr-3 font-mono text-foreground whitespace-nowrap">
                      {ev.task_class}
                    </td>
                    <td className="py-1.5 pr-3 text-muted-foreground whitespace-nowrap">
                      {ev.account_id}
                    </td>
                    <td className="py-1.5 text-muted-foreground">{ev.reason}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}
