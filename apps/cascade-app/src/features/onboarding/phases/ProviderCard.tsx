/**
 * Purpose: Individual AI provider card for the wizard's Connect AI step.
 * Inputs:  ProviderCardProps — provider metadata, connection status, event callbacks.
 * Outputs: A styled card with provider name, icon placeholder, status badge, and
 *          a Connect button (or "Connected" indicator when already connected).
 * Constraints: No direct Tauri invoke calls — parent (ProviderConnectPhase) owns
 *   all async operations. Error display is inline within the card.
 *   Accessible: role="region", aria-label per card, button has descriptive aria-label.
 * SPORT: MASTER-COMPONENTS.md — ProviderCard
 */

import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Supported AI provider identifiers. */
export type ProviderId = 'gemini' | 'anthropic' | 'openai' | 'opencode'

export interface ProviderInfo {
  id: ProviderId
  name: string
  /** Short tagline shown below the name. */
  tagline: string
  /** Accent color class for the icon placeholder (Tailwind bg-* class). */
  iconColor: string
}

export type ConnectionStatus =
  | 'disconnected'
  | 'connecting'
  | 'connected'
  | 'pool_detected'
  | 'error'

export interface ProviderCardProps {
  provider: ProviderInfo
  status: ConnectionStatus
  /** Error message to display inline (only when status === 'error'). */
  errorMessage?: string
  /** Download progress 0-100 when a local model is downloading (status === 'connecting'). */
  downloadProgress?: number
  /** Called when the user presses the Connect button. */
  onConnect: (id: ProviderId) => void
}

// ---------------------------------------------------------------------------
// Status badge
// ---------------------------------------------------------------------------

interface BadgeProps {
  status: ConnectionStatus
}

function StatusBadge({ status }: BadgeProps) {
  const configs: Record<
    ConnectionStatus,
    { label: string; className: string }
  > = {
    disconnected: {
      label: 'Not connected',
      className: 'bg-muted text-muted-foreground',
    },
    connecting: {
      label: 'Connecting…',
      className: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/40 dark:text-yellow-300',
    },
    connected: {
      label: 'Connected',
      className: 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300',
    },
    pool_detected: {
      label: 'Gemini Pool detected',
      className: 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300',
    },
    error: {
      label: 'Connection failed',
      className: 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300',
    },
  }
  const { label, className } = configs[status]
  return (
    <span
      className={cn(
        'inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium',
        className
      )}
    >
      {label}
    </span>
  )
}

// ---------------------------------------------------------------------------
// ProviderCard
// ---------------------------------------------------------------------------

/**
 * Purpose: Render a single provider card inside the Connect AI wizard step.
 *
 * Inputs:
 *   - provider: static metadata (id, name, tagline, iconColor)
 *   - status: live connection state driven by parent
 *   - errorMessage: inline error string (shown when status === 'error')
 *   - onConnect: parent callback invoked on button press
 *
 * Outputs: A card element with accessible region landmark and status badge.
 *
 * Constraints:
 *   - No async calls inside this component.
 *   - Button is disabled when status is 'connecting' or 'connected' or 'pool_detected'.
 *
 * SPORT: MASTER-COMPONENTS.md — ProviderCard
 */
export function ProviderCard({
  provider,
  status,
  errorMessage,
  onConnect,
}: ProviderCardProps) {
  const isConnected = status === 'connected' || status === 'pool_detected'
  const isConnecting = status === 'connecting'
  const buttonDisabled = isConnected || isConnecting

  return (
    <div
      role="region"
      aria-label={`${provider.name} provider`}
      className={cn(
        'flex flex-col gap-3 rounded-lg border bg-card p-4 shadow-sm transition-colors',
        isConnected && 'border-green-400 dark:border-green-700',
        status === 'error' && 'border-red-400 dark:border-red-700'
      )}
    >
      {/* Header row: icon + name + badge */}
      <div className="flex items-center gap-3">
        {/* Provider icon placeholder */}
        <div
          aria-hidden="true"
          className={cn(
            'flex h-10 w-10 shrink-0 items-center justify-center rounded-md text-white text-sm font-bold',
            provider.iconColor
          )}
        >
          {provider.name.charAt(0)}
        </div>

        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-semibold text-sm leading-tight">
              {provider.name}
            </span>
            <StatusBadge status={status} />
          </div>
          <p className="mt-0.5 text-xs text-muted-foreground leading-snug">
            {provider.tagline}
          </p>
        </div>
      </div>

      {/* Inline error message */}
      {status === 'error' && errorMessage && (
        <p
          role="alert"
          className="rounded-md bg-red-50 px-3 py-2 text-xs text-red-700 dark:bg-red-900/30 dark:text-red-300"
        >
          {errorMessage}
        </p>
      )}

      {/* Connect button */}
      {!isConnected && (
        <button
          type="button"
          disabled={buttonDisabled}
          aria-label={`Connect ${provider.name}`}
          onClick={() => onConnect(provider.id)}
          className={cn(
            'mt-1 inline-flex h-8 items-center justify-center rounded-md px-4 text-xs font-medium',
            'border border-input bg-background shadow-sm',
            'hover:bg-accent hover:text-accent-foreground',
            'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
            'disabled:pointer-events-none disabled:opacity-50',
            'transition-colors'
          )}
        >
          {isConnecting ? 'Connecting…' : 'Connect'}
        </button>
      )}
    </div>
  )
}
