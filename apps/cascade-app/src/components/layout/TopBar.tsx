/**
 * Purpose: Top application bar — app icon, title, spacer, connection indicator, ThemeToggle.
 * Inputs:  None (reads useDaemonStatus + useAccounts + useThemeStore internally).
 * Outputs: <header role="banner"> landmark fixed to the top of the app chrome.
 * Constraints: role="banner" for accessibility. Connection dot is a 8px circle:
 *   green (connected), amber (connecting), red (disconnected/error).
 *   Quota-store freshness (useAccounts().isLive) overrides a failing daemon
 *   ping — `accounts_list` reads quota.json directly and has no dependency
 *   on the RPC health-check, so the app must never show "Disconnected" while
 *   the account data itself is fresh.
 * SPORT: MASTER-COMPONENTS.md — TopBar
 */

import { MountainSnow } from 'lucide-react'
import { useDaemonStatus } from '../../store/index'
import { useAccounts } from '../../features/accounts/useAccounts'
import { ThemeToggle } from '../ThemeToggle'

const STATUS_DOT: Record<string, string> = {
  connected: 'bg-green-500',
  connecting: 'bg-amber-400',
  disconnected: 'bg-red-500',
  error: 'bg-red-500',
}

const STATUS_TEXT: Record<string, string> = {
  connected: 'text-green-500',
  connecting: 'text-amber-400',
  disconnected: 'text-red-500',
  error: 'text-red-500',
}

const STATUS_LABEL: Record<string, string> = {
  connected: 'Connected',
  connecting: 'Connecting…',
  disconnected: 'Disconnected',
  error: 'Disconnected',
}

const STATUS_ARIA: Record<string, string> = {
  connected: 'Daemon connected',
  connecting: 'Connecting to daemon',
  disconnected: 'Daemon disconnected',
  error: 'Daemon error',
}

export function TopBar() {
  const { status } = useDaemonStatus()
  const { isLive } = useAccounts()
  // Quota-store freshness wins over the raw daemon ping (see file header).
  const effectiveStatus = isLive ? 'connected' : status
  const dotClass = STATUS_DOT[effectiveStatus] ?? 'bg-muted-foreground'
  const textClass = STATUS_TEXT[effectiveStatus] ?? 'text-muted-foreground'
  const statusLabel = STATUS_LABEL[effectiveStatus] ?? effectiveStatus
  const ariaLabel = STATUS_ARIA[effectiveStatus] ?? effectiveStatus

  return (
    <header
      role="banner"
      className="flex h-12 shrink-0 items-center gap-3 border-b border-border bg-background px-4"
    >
      {/* App icon */}
      <MountainSnow className="h-5 w-5 shrink-0" style={{ color: '#4F9BE8' }} aria-hidden="true" />

      {/* App title */}
      <span className="text-sm font-semibold tracking-tight">Cascade</span>

      {/* Spacer */}
      <div className="flex-1" aria-hidden="true" />

      {/* Connection indicator */}
      <div className="flex items-center gap-1.5" aria-label={ariaLabel}>
        <span className={['h-2 w-2 rounded-full', dotClass].join(' ')} aria-hidden="true" />
        <span className={['text-xs', textClass].join(' ')}>{statusLabel}</span>
      </div>

      {/* Theme toggle */}
      <ThemeToggle />
    </header>
  )
}
