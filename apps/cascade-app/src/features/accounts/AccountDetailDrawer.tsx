/**
 * Purpose: Slide-in detail drawer for a single account — full quota breakdown
 *   across every window (5h, 7d, 7d-opus, 7d-sonnet, monthly credit) plus
 *   re-auth / remove actions (Epic 1, T1.3).
 * Inputs:  account (or null = closed), onClose, onReauth, onRemove, busy.
 * Outputs: A fixed right-edge panel with backdrop; null when no account.
 * Constraints:
 *   - No Sheet primitive exists in this app; implemented as a fixed panel.
 *   - GFP (gfp) accounts show key-count from extra_usage where present.
 *   - Re-auth button only rendered for providers that support it.
 * SPORT: MASTER-COMPONENTS.md — AccountDetailDrawer
 */

import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  accountLabel,
  canReauth,
  gfpCapacity,
  isGfpPool,
  pct,
  statusColor,
  utilColor,
  type AccountQuota,
  type QuotaWindow,
} from './types'

interface AccountDetailDrawerProps {
  /** The account to show, or null when the drawer is closed. */
  account: AccountQuota | null
  /** Close the drawer. */
  onClose: () => void
  /** Trigger re-auth for this account id. */
  onReauth: (accountId: string) => void
  /** Trigger removal for this account id. */
  onRemove: (accountId: string) => void
  /** Disable action buttons while an action is in flight. */
  busy?: boolean
}

/** One labelled quota window row. */
function WindowRow({
  label,
  win,
}: {
  label: string
  win?: QuotaWindow | null
}): React.ReactElement {
  return (
    <div className="flex items-center justify-between border-b border-border/50 py-2">
      <span className="text-sm text-muted-foreground">{label}</span>
      <div className="text-right">
        <span className={`text-sm font-medium ${utilColor(win?.utilization)}`}>
          {pct(win?.utilization)}
        </span>
        {win?.resets_in && (
          <span className="ml-2 text-xs text-muted-foreground">resets {win.resets_in}</span>
        )}
      </div>
    </div>
  )
}

/**
 * Right-edge detail drawer. Renders nothing when `account` is null.
 */
export function AccountDetailDrawer({
  account,
  onClose,
  onReauth,
  onRemove,
  busy = false,
}: AccountDetailDrawerProps): React.ReactElement | null {
  if (!account) return null

  const usage = account.usage
  const extra = usage?.extra_usage
  const pool = isGfpPool(account)

  return (
    <>
      {/* Backdrop */}
      <div className="fixed inset-0 z-40 bg-black/40" aria-hidden="true" onClick={onClose} />

      {/* Panel */}
      <aside
        role="dialog"
        aria-label={`Account detail: ${accountLabel(account)}`}
        className="fixed right-0 top-0 z-50 flex h-full w-96 max-w-full flex-col border-l border-border bg-card shadow-xl"
      >
        {/* Header */}
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <div className="flex items-center gap-2">
            <span className="rounded bg-primary px-2 py-0.5 text-xs font-semibold text-primary-foreground">
              {accountLabel(account)}
            </span>
            <span className="text-sm font-medium">{account.provider}</span>
            <span
              className={`rounded px-1.5 py-0.5 text-xs font-medium ${statusColor(account.status)}`}
            >
              {account.status ?? 'unknown'}
            </span>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close detail"
            className="rounded p-1 text-muted-foreground hover:bg-accent/50 hover:text-foreground"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        {/* Body */}
        <div className="flex-1 overflow-auto px-4 py-3">
          {account.email && (
            <p className="mb-3 break-words text-xs text-muted-foreground">{account.email}</p>
          )}

          {pool ? (
            /* GFP pool — no per-account quota; show static capacity info */
            <>
              <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                Pool capacity
              </h3>
              <div className="flex items-center justify-between border-b border-border/50 py-2">
                <span className="text-sm text-muted-foreground">Keys</span>
                <span className="text-sm">{account.key_count ?? '—'}</span>
              </div>
              <div className="flex items-center justify-between border-b border-border/50 py-2">
                <span className="text-sm text-muted-foreground">Est. req/day</span>
                <span className="text-sm">
                  {account.key_count ? `~${account.key_count * 1500}` : '—'}
                </span>
              </div>
              <div className="flex items-center justify-between border-b border-border/50 py-2">
                <span className="text-sm text-muted-foreground">RPM (pool)</span>
                <span className="text-sm">
                  {account.key_count ? `${account.key_count * 15}` : '—'}
                </span>
              </div>
              <p className="mt-3 text-xs text-muted-foreground/70 italic">
                {gfpCapacity(account.key_count)} · Gemini Flash free tier · round-robin · 1,500
                req/day per key · 15 RPM per key
              </p>
            </>
          ) : (
            <>
              <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                Quota windows
              </h3>
              <WindowRow label="5-hour" win={usage?.five_hour} />
              <WindowRow label="7-day" win={usage?.seven_day} />
              <WindowRow label="7-day (Opus)" win={usage?.seven_day_opus} />
              <WindowRow label="7-day (Sonnet)" win={usage?.seven_day_sonnet} />

              {extra && extra.is_enabled && (
                <>
                  <h3 className="mb-1 mt-4 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                    Monthly credit
                  </h3>
                  <div className="flex items-center justify-between border-b border-border/50 py-2">
                    <span className="text-sm text-muted-foreground">Utilization</span>
                    <span className={`text-sm font-medium ${utilColor(extra.utilization)}`}>
                      {pct(extra.utilization)}
                    </span>
                  </div>
                  {extra.used_credits != null && extra.monthly_limit != null && (
                    <div className="flex items-center justify-between border-b border-border/50 py-2">
                      <span className="text-sm text-muted-foreground">Credits</span>
                      <span className="text-sm">
                        {extra.used_credits.toFixed(extra.decimal_places ?? 0)} /{' '}
                        {extra.monthly_limit.toFixed(extra.decimal_places ?? 0)}{' '}
                        {extra.currency ?? ''}
                      </span>
                    </div>
                  )}
                </>
              )}
            </>
          )}
        </div>

        {/* Footer actions */}
        <div className="flex gap-2 border-t border-border px-4 py-3">
          {canReauth(account.provider) && (
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => onReauth(account.account)}
            >
              Re-auth
            </Button>
          )}
          <Button
            variant="destructive"
            size="sm"
            disabled={busy}
            onClick={() => onRemove(account.account)}
          >
            Remove
          </Button>
        </div>
      </aside>
    </>
  )
}
