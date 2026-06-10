/**
 * Purpose: Centralised React Router v6 route table for the Cascade app.
 * Inputs:  Consumed by App.tsx inside <Routes>. AppLayout wraps authenticated routes.
 * Outputs: Array of RouteObject plus a RouterApp helper component.
 * Constraints: /onboarding is NOT wrapped in AppLayout (standalone wizard).
 *   Navigate redirect uses replace=true to avoid stacking history entries.
 *   RouterApp receives isLoading and launchWizard from App so the wizard redirect
 *   happens inside Router context (avoids Navigate-outside-Router invariant violation).
 * SPORT: MASTER-ROUTES.md — all frontend routes
 */

import { Navigate, Route, Routes } from 'react-router-dom'
import { AppLayout } from '../layouts/AppLayout'
import { DashboardPage } from '../pages/DashboardPage'
import { InboxPage } from '../pages/InboxPage'
import { NotFoundPage } from '../pages/NotFoundPage'
import { SearchPage } from '../pages/SearchPage'
import { SettingsPage } from '../pages/SettingsPage'
import { ProviderSettings } from '../pages/ProviderSettings'
import { UsagePage } from '../pages/UsagePage'
import { TemplateBrowser } from '../pages/TemplateBrowser'
import { WizardLayout } from '../features/onboarding/WizardLayout'

interface RouterAppProps {
  /** True while the wizard status check is in-flight. */
  isLoading: boolean
  /** True when the wizard should be shown (NeverRun or InProgress status). */
  launchWizard: boolean
}

/**
 * Purpose: Renders the full route tree inside the parent BrowserRouter.
 *   Authenticated pages are wrapped in AppLayout (chrome).
 *   /onboarding is standalone — no sidebar/topbar.
 *   Wizard routing guard lives here (inside Router context) so Navigate is valid.
 * Inputs:  isLoading, launchWizard flags from App (derived from useWizardLaunch).
 * Outputs: <Routes> element with 7 routes + loading / wizard redirect guards.
 * Constraints: Must be rendered inside a Router provider.
 * SPORT: MASTER-ROUTES.md
 */
export function RouterApp({ isLoading, launchWizard }: RouterAppProps) {
  // Show loading screen while checking wizard status — rendered inside Router
  // context so any child hooks that need Router (e.g. useLocation) still work.
  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-screen bg-background">
        <div className="text-lg text-muted-foreground">Loading...</div>
      </div>
    )
  }

  return (
    <Routes>
      {/*
       * Wizard guard: if wizard is NeverRun or InProgress, redirect all traffic
       * to /onboarding. This Navigate runs inside Router context — safe.
       */}
      {launchWizard && <Route path="*" element={<Navigate to="/onboarding" replace />} />}

      {/* Root → redirect to dashboard */}
      <Route path="/" element={<Navigate to="/dashboard" replace />} />

      {/* Standalone routes — no app chrome */}
      <Route path="/onboarding" element={<WizardLayout />} />

      {/* Authenticated routes — wrapped in persistent app chrome */}
      <Route element={<AppLayout />}>
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/inbox" element={<InboxPage />} />
        <Route path="/search" element={<SearchPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        {/* T-P3-E04-23: Provider Settings page */}
        <Route path="/settings/providers" element={<ProviderSettings />} />
        {/* T-P3-E04-30: Usage analytics page */}
        <Route path="/usage" element={<UsagePage />} />
        {/* T-P3-E05-14: Template browser */}
        <Route path="/templates" element={<TemplateBrowser />} />
      </Route>

      {/* Catch-all */}
      <Route path="*" element={<NotFoundPage />} />
    </Routes>
  )
}
