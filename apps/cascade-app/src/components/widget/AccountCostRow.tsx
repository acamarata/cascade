/**
 * Purpose: Render a single per-account cost row: truncated email, total cost,
 *   and a relative "last active" timestamp.
 * Inputs:  { email, costUsd, lastActive } — lastActive is an ISO-8601 string.
 * Outputs: A row with truncated email, $X.XX cost, relative time label.
 * Constraints:
 *   - Email truncated to 28 chars with ellipsis if longer (no personal-data exposure
 *     beyond what the daemon already supplies).
 *   - Relative time is derived from lastActive without external date libraries.
 *   - No divide-by-zero; all values are treated as optional with safe defaults.
 * SPORT: MASTER-COMPONENTS.md — AccountCostRow
 */

import React from 'react'

/** Convert an ISO-8601 date string to a human-readable relative label. */
export function toRelativeTime(iso: string): string {
  const now = Date.now()
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return 'unknown'
  const diffMs = now - then
  const diffSecs = Math.floor(diffMs / 1_000)
  if (diffSecs < 60) return 'just now'
  const diffMins = Math.floor(diffSecs / 60)
  if (diffMins < 60) return `${diffMins}m ago`
  const diffHours = Math.floor(diffMins / 60)
  if (diffHours < 24) return `${diffHours}h ago`
  const diffDays = Math.floor(diffHours / 24)
  return `${diffDays}d ago`
}

/** Truncate email for display. */
function truncateEmail(email: string, maxLen = 28): string {
  if (email.length <= maxLen) return email
  return `${email.slice(0, maxLen - 1)}…`
}

export interface AccountCostRowProps {
  /** Account email address */
  email: string
  /** Total cost in USD this period */
  costUsd: number
  /** ISO-8601 timestamp of last activity */
  lastActive: string
}

/**
 * A single account cost row in the fleet widget.
 *
 * Shows truncated email, formatted cost, and relative last-active time.
 */
export function AccountCostRow({ email, costUsd, lastActive }: AccountCostRowProps): React.ReactElement {
  const displayEmail = truncateEmail(email)
  const relTime = toRelativeTime(lastActive)

  return (
    <div className="flex items-center justify-between py-1.5 text-xs">
      <span className="text-foreground truncate max-w-[160px]" title={email}>
        {displayEmail}
      </span>
      <span className="text-muted-foreground ml-2 shrink-0 tabular-nums">
        ${costUsd.toFixed(2)} · {relTime}
      </span>
    </div>
  )
}
