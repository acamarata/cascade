/**
 * Purpose: Persistent app chrome wrapper — composes Sidebar, TopBar, page content,
 *   StatusBar, and CommandPalette into the full authenticated-app shell.
 * Inputs:  None (child route content is injected via <Outlet />).
 * Outputs: Full-height flex layout: Sidebar (left) + column (TopBar / content / StatusBar),
 *   plus a portal-rendered CommandPalette that opens on Cmd+K / Ctrl+K.
 * Constraints: Only used for authenticated routes (/dashboard, /inbox, /search, /settings).
 *   /onboarding and 404 are NOT wrapped by AppLayout.
 *   registerDefaultCommands is called once per mount (idempotent — safe to remount).
 *   CommandPalette receives navigate and setTheme via registry; not via direct props.
 * SPORT: MASTER-COMPONENTS.md — AppLayout
 */

import { useEffect } from 'react';
import { Outlet, useNavigate } from 'react-router-dom';
import { CommandPalette } from '../components/CommandPalette';
import { RouteAnnouncer } from '../components/RouteAnnouncer';
import { SkipToMain } from '../components/SkipToMain';
import { Sidebar } from '../components/layout/Sidebar';
import { StatusBar } from '../components/layout/StatusBar';
import { TopBar } from '../components/layout/TopBar';
import { useCommandPalette } from '../hooks/useCommandPalette';
import { registerDefaultCommands } from '../lib/commands/registry';
import { useThemeStore } from '../store';

export function AppLayout() {
  const navigate = useNavigate();
  const { isOpen, close } = useCommandPalette();
  const { setTheme } = useThemeStore();

  // Register default nav + theme commands once per mount. Idempotent — re-registration
  // replaces by id so remounts (HMR, StrictMode double-invoke) have no side-effects.
  useEffect(() => {
    registerDefaultCommands(navigate, setTheme);
  }, [navigate, setTheme]);

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

      {/* Global command palette — portal-rendered, triggered by Cmd+K / Ctrl+K */}
      <CommandPalette isOpen={isOpen} onClose={close} />
    </div>
  );
}
