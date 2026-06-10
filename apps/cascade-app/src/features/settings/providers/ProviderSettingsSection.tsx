/**
 * Purpose: Inline SettingsPanel section for AI providers.
 *   Shows a summary of connected providers (count + first few names) and a
 *   "Manage Providers" link to /settings/providers, plus a quick-add button
 *   that opens AddProviderDialog.
 * Inputs:  None — reads from cascade_providers_health IPC (via useProviderConnected)
 * Outputs: <section> that mounts additively inside SettingsPanel
 * Constraints:
 *   - This section is purely informational + navigation; full management lives at
 *     /settings/providers (ProviderSettings page).
 *   - Never throws — all IPC errors resolve to empty state (AI-optional principle).
 *   - WCAG 2.1 AA: landmark section, focus ring on links and buttons.
 * SPORT: MASTER-COMPONENTS.md — ProviderSettingsSection — features/settings/providers/ — T-P3-E04-23
 */

import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useProviderConnected } from '@/hooks/useProviderConnected'
import { AddProviderDialog } from '@/components/providers/AddProviderDialog'
import { Button } from '@/components/ui/button'
import { CheckCircle2, AlertCircle, Plus, ChevronRight } from 'lucide-react'

/**
 * Compact settings-panel section summarising AI provider status.
 *
 * Renders as a section with:
 *   - Status summary (connected count or "No providers connected")
 *   - Link to full /settings/providers management page
 *   - Quick "Add Provider" button that opens AddProviderDialog
 *
 * On successful add, re-reads provider health to update the summary.
 */
export function ProviderSettingsSection() {
  const { anyConnected, connectedProviders } = useProviderConnected()
  const [dialogOpen, setDialogOpen] = useState(false)

  function handleAddSuccess() {
    // useProviderConnected re-polls on window focus; close the dialog and the
    // hook will update on next focus/interval cycle. For an immediate update,
    // dispatch a synthetic focus event.
    window.dispatchEvent(new Event('focus'))
  }

  return (
    <section aria-labelledby="providers-section-heading" className="space-y-3">
      <h2 id="providers-section-heading" className="text-sm font-medium">
        AI Providers
      </h2>

      <div className="flex items-center justify-between rounded-lg border border-border bg-card px-4 py-3 gap-4">
        {/* Status summary */}
        <div className="flex items-center gap-2 min-w-0">
          {anyConnected ? (
            <>
              <CheckCircle2
                className="h-4 w-4 shrink-0 text-green-600 dark:text-green-400"
                aria-hidden="true"
              />
              <span className="text-sm truncate">
                {connectedProviders.length === 1
                  ? `1 provider connected`
                  : `${connectedProviders.length} providers connected`}
              </span>
            </>
          ) : (
            <>
              <AlertCircle
                className="h-4 w-4 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
              <span className="text-sm text-muted-foreground">No providers connected</span>
            </>
          )}
        </div>

        {/* Actions */}
        <div className="flex items-center gap-1 shrink-0">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setDialogOpen(true)}
            aria-label="Add a new AI provider"
            className="gap-1"
          >
            <Plus className="h-4 w-4" aria-hidden="true" />
            Add
          </Button>
          <Button variant="ghost" size="sm" asChild className="gap-1">
            <Link to="/settings/providers" aria-label="Manage all AI providers">
              Manage
              <ChevronRight className="h-4 w-4" aria-hidden="true" />
            </Link>
          </Button>
        </div>
      </div>

      <AddProviderDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onSuccess={handleAddSuccess}
      />
    </section>
  )
}
