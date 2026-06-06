/**
 * Purpose: Settings placeholder page — replaced by E-03 settings panel content.
 *   Also hosts a demo button (T-P3-E01-17) that opens a secondary Tauri window.
 * Inputs:  None.
 * Outputs: <main> with h1 heading + demo multi-window button.
 * Constraints: Demo button exercises openWindow IPC; safe to call repeatedly
 *   (Rust side does check-or-focus). Not final E-03 UI.
 * SPORT: MASTER-ROUTES.md — /settings
 */

import { Button } from '@/components/ui/button'
import { useAppStore } from '@/store'

export function SettingsPage() {
  const openWindow = useAppStore((s) => s.openWindow)

  return (
    <main className="flex-1 p-6 space-y-4">
      <h1 className="text-2xl font-semibold">Settings</h1>
      {/* T-P3-E01-17 demo: open a secondary Tauri window */}
      <Button
        variant="outline"
        onClick={() => openWindow('settings-panel', '/settings', 'Cascade Settings')}
      >
        Open in new window
      </Button>
    </main>
  )
}
