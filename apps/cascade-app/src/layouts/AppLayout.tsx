/**
 * Purpose: Persistent app chrome wrapper — composes Sidebar, TopBar, page content,
 *   and StatusBar into the full authenticated-app shell.
 * Inputs:  None (child route content is injected via <Outlet />).
 * Outputs: Full-height flex layout: Sidebar (left) + column (TopBar / content / StatusBar).
 * Constraints: Only used for authenticated routes (/dashboard, /inbox, /search, /settings).
 *   /onboarding and 404 are NOT wrapped by AppLayout.
 *   registerDefaultCommands is called once per mount (idempotent — safe to remount).
 *   CommandPalette is rendered in App.tsx (above all routes) — not here.
 * SPORT: MASTER-COMPONENTS.md — AppLayout
 */

import { useEffect } from 'react'
import { Outlet, useNavigate } from 'react-router-dom'
import { RouteAnnouncer } from '../components/RouteAnnouncer'
import { SkipToMain } from '../components/SkipToMain'
import { Sidebar } from '../components/layout/Sidebar'
import { StatusBar } from '../components/layout/StatusBar'
import { TopBar } from '../components/layout/TopBar'
import { registerDefaultCommands } from '../lib/commands/registry'
import { useDaemonStatus, useThemeStore } from '../store'

export function AppLayout() {
  const navigate = useNavigate()
  const { setTheme } = useThemeStore()
  const { startPolling } = useDaemonStatus()

  // Register default nav + theme commands once per mount. Idempotent — re-registration
  // replaces by id so remounts (HMR, StrictMode double-invoke) have no side-effects.
  // Also kicks off daemon status polling so StatusBar + TopBar reflect live state.
  useEffect(() => {
    registerDefaultCommands(navigate, setTheme)
    startPolling()
  }, [navigate, setTheme, startPolling])

  return (
    <div className="flex h-screen overflow-hidden bg-background text-foreground">
      {/* Keyboard a11y: skip-to-content link (visible on focus) + route live-region */}
      <SkipToMain />
      <RouteAnnouncer />

      {/* Left: persistent navigation */}
      <Sidebar />

      {/* Right: stacked top-bar / page / status-bar */}
      <div className="flex flex-1 flex-col overflow-hidden">
        <TopBar />

        <main id="main-content" tabIndex={-1} className="flex-1 overflow-auto focus:outline-none">
          <Outlet />
        </main>

        <StatusBar />
      </div>
    </div>
  )
}
