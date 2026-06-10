/**
 * Purpose: Full settings surface — mounts the tabbed SettingsPanel (T-P3-E07-15/16).
 *   Replaces the placeholder with the two-pane sidebar + tab content layout.
 *   The "Open in new window" affordance is preserved as a secondary action.
 * Inputs:  None.
 * Outputs: Full-height <main> containing SettingsPanel (features/settings/).
 * Constraints:
 *   - SettingsPanel manages its own IPC (get_settings / update_settings).
 *   - Page must be full-height so the sidebar + scroll area work correctly.
 *   - The legacy src/components/SettingsPanel is not deleted (non-destructive).
 * SPORT: MASTER-ROUTES.md — /settings (T-P3-E07-15)
 */

import { SettingsPanel } from '@/features/settings/SettingsPanel'

export function SettingsPage() {
  return (
    <main className="flex flex-col h-full overflow-hidden" aria-label="Settings">
      <SettingsPanel />
    </main>
  )
}
