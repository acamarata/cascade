/**
 * Purpose: Displays a coloured status badge for an AI provider connection.
 * Inputs:  status — ProviderStatus ('healthy' | 'unhealthy' | 'unknown')
 *          errorMsg — optional error detail shown in tooltip when unhealthy
 * Outputs: <span> badge with semantic colour and screen-reader text
 * Constraints:
 *   - Three visual states: green/healthy, red/unhealthy, grey/unknown.
 *   - WCAG 2.1 AA: colour alone is insufficient; badge includes icon + text.
 *   - Zero runtime deps beyond existing UI primitives.
 * SPORT: MASTER-COMPONENTS.md — ProviderStatusBadge — components/providers/ — T-P3-E04-23
 */

import { CheckCircle2, XCircle, HelpCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { ProviderStatus } from '@/types/providers'

interface ProviderStatusBadgeProps {
  status: ProviderStatus
  errorMsg?: string
}

const STATUS_CONFIG: Record<
  ProviderStatus,
  { label: string; icon: React.ElementType; classes: string }
> = {
  healthy: {
    label: 'Connected',
    icon: CheckCircle2,
    classes: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400',
  },
  unhealthy: {
    label: 'Error',
    icon: XCircle,
    classes: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400',
  },
  unknown: {
    label: 'Unconfigured',
    icon: HelpCircle,
    classes: 'bg-muted text-muted-foreground',
  },
}

/**
 * Coloured status badge with icon and accessible label.
 *
 * Shows 'Connected' (green), 'Error' (red), or 'Unconfigured' (grey).
 * When status is unhealthy and errorMsg is provided, renders the error as a
 * `title` attribute so it is accessible on hover/focus.
 *
 * @param status - ProviderStatus from the daemon health check
 * @param errorMsg - Optional error detail string (shown when status === 'unhealthy')
 */
export function ProviderStatusBadge({ status, errorMsg }: ProviderStatusBadgeProps) {
  const cfg = STATUS_CONFIG[status]
  const Icon = cfg.icon

  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium',
        cfg.classes,
      )}
      title={status === 'unhealthy' && errorMsg ? errorMsg : undefined}
      role="status"
      aria-label={`Provider status: ${cfg.label}${status === 'unhealthy' && errorMsg ? ` — ${errorMsg}` : ''}`}
    >
      <Icon className="h-3 w-3" aria-hidden="true" />
      {cfg.label}
    </span>
  )
}
