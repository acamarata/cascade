/**
 * Purpose: TypeScript mirror types for the accounts IPC commands (Epic 1).
 *   Mirrors the Rust wire types in src-tauri/src/commands/accounts.rs.
 * Inputs:  Consumed by AccountsPage, AccountDetailDrawer, useAccounts.
 * Outputs: Exported interfaces + a small set of label/color helpers.
 * Constraints: Keep in sync with quota.json. Fields are nullable where the
 *   source file emits null (opaque providers, disabled credit blocks).
 * SPORT: MASTER-LIBS.md — accounts types, src/features/accounts/types.ts
 */

/** One rolling-window utilization figure (5h, 7d, etc.). */
export interface QuotaWindow {
  resets_at?: number | null
  resets_in?: string | null
  utilization?: number | null
}

/** Monthly extra-usage (credit) block. */
export interface ExtraUsage {
  currency?: string | null
  monthly_limit?: number | null
  used_credits?: number | null
  utilization?: number | null
  is_enabled?: boolean | null
  disabled_reason?: string | null
  daily?: unknown
  weekly?: unknown
  decimal_places?: number | null
}

/** Per-account usage breakdown. */
export interface AccountUsage {
  five_hour?: QuotaWindow | null
  seven_day?: QuotaWindow | null
  seven_day_opus?: QuotaWindow | null
  seven_day_sonnet?: QuotaWindow | null
  extra_usage?: ExtraUsage | null
}

/** One account row from quota.json. */
export interface AccountQuota {
  account: string
  provider: string
  email?: string | null
  status?: string | null
  last_pull_at?: number | null
  quota_opaque?: boolean | null
  usage?: AccountUsage | null
}

/** Top-level quota.json shape. */
export interface AccountsQuota {
  accounts: AccountQuota[]
}

/** Provider id → short two-letter label used across the widget + desktop UI. */
export function accountLabel(account: AccountQuota): string {
  const id = account.account.toLowerCase()
  switch (account.provider) {
    case 'claude':
      // claude-acc1 → A1, claude-acc2 → A2 …
      return `A${id.replace(/\D/g, '') || '1'}`
    case 'codex':
      return 'Co'
    case 'gemini':
      return 'Ge'
    case 'opencode':
      return 'OC'
    case 'gfp':
      return 'GP'
    default:
      return account.provider.slice(0, 2).toUpperCase()
  }
}

/**
 * Tailwind text-color class for a utilization percentage.
 * green <50, yellow 50–80, red >80 — matches the widget convention.
 */
export function utilColor(util?: number | null): string {
  if (util == null) return 'text-muted-foreground'
  if (util > 80) return 'text-red-500'
  if (util >= 50) return 'text-yellow-500'
  return 'text-green-500'
}

/** Status → badge color classes. ok=green, auth_dead=red, else=yellow. */
export function statusColor(status?: string | null): string {
  switch (status) {
    case 'ok':
      return 'bg-green-500/15 text-green-500'
    case 'auth_dead':
      return 'bg-red-500/15 text-red-500'
    default:
      return 'bg-yellow-500/15 text-yellow-500'
  }
}

/** Format a 0–100 utilization as "NN%", or "—" when absent. */
export function pct(util?: number | null): string {
  return util == null ? '—' : `${Math.round(util)}%`
}

/** True for providers that support an interactive re-auth helper. */
export function canReauth(provider: string): boolean {
  return provider === 'claude' || provider === 'gemini'
}
