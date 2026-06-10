/**
 * Purpose: Renders a single row in the ProviderSettings list.
 *   Shows provider name, auth method, status badge, today's cost, and actions.
 * Inputs:
 *   - item: ProviderListItem — data from cascade_providers_list
 *   - onTest: () => void — wired to cascade_providers_test IPC
 *   - onRemove: () => void — wired to cascade_providers_remove IPC
 *   - pending: boolean — true while a test or remove is in-flight
 * Outputs: <article> card with all provider metadata
 * Constraints:
 *   - todayCostUsd undefined renders a dash, not an error.
 *   - Uses shadcn/ui Card styling conventions (border, padding) without importing
 *     a dedicated Card primitive (avoids adding a new dep not yet in ui/).
 *   - WCAG 2.1 AA: interactive elements have focus rings and aria labels.
 * SPORT: MASTER-COMPONENTS.md — ProviderCard — components/providers/ — T-P3-E04-23
 */

import { Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { ProviderStatusBadge } from './ProviderStatusBadge'
import { ProviderActionsMenu } from './ProviderActionsMenu'
import type { ProviderListItem } from '@/types/providers'

interface ProviderCardProps {
  item: ProviderListItem
  onTest: () => void
  onRemove: () => void
  /** True while an async action is in-flight for this provider. */
  pending?: boolean
}

/**
 * Single provider row card displaying all ProviderListItem fields with action buttons.
 *
 * When pending=true a spinner overlays the card and actions are disabled,
 * preventing duplicate invocations while a test or remove is in-flight.
 *
 * @param item - Provider data from cascade_providers_list IPC
 * @param onTest - Fires cascade_providers_test for this provider
 * @param onRemove - Fires cascade_providers_remove for this provider
 * @param pending - Disables UI during in-flight async op
 */
export function ProviderCard({ item, onTest, onRemove, pending = false }: ProviderCardProps) {
  const authMethodLabel = item.authMethod === 'api_key' ? 'API Key' : item.authMethod === 'oauth' ? 'OAuth' : 'No Auth'

  return (
    <article
      aria-label={`${item.name} provider`}
      className={cn(
        'flex items-center justify-between rounded-lg border border-border bg-card px-4 py-3 gap-4 transition-opacity',
        pending && 'opacity-70',
      )}
    >
      {/* Left: name + meta */}
      <div className="flex flex-col gap-0.5 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-sm font-medium truncate">{item.name}</span>
          <ProviderStatusBadge status={item.status} errorMsg={item.errorMsg} />
        </div>
        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          <span>{authMethodLabel}</span>
          <span aria-hidden="true">·</span>
          <span aria-label={`Today's cost: ${item.todayCostUsd !== undefined ? `$${item.todayCostUsd.toFixed(4)}` : 'unavailable'}`}>
            {item.todayCostUsd !== undefined ? `$${item.todayCostUsd.toFixed(4)} today` : '—'}
          </span>
        </div>
        {item.status === 'unhealthy' && item.errorMsg && (
          <p className="text-xs text-destructive mt-0.5 truncate" title={item.errorMsg}>
            {item.errorMsg}
          </p>
        )}
      </div>

      {/* Right: pending spinner or actions */}
      {pending ? (
        <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground" aria-label="Working…" />
      ) : (
        <ProviderActionsMenu
          providerId={item.id}
          onTestConnection={onTest}
          onRemove={onRemove}
        />
      )}
    </article>
  )
}
