/**
 * Purpose: Centralised React Router v6 route table for the Cascade app.
 * Inputs:  Consumed by App.tsx inside <Routes>. AppLayout wraps authenticated routes.
 * Outputs: Array of RouteObject plus a RouterApp helper component.
 * Constraints: /onboarding is NOT wrapped in AppLayout (standalone wizard).
 *   Navigate redirect uses replace=true to avoid stacking history entries.
 *   RouterApp receives isLoading and launchWizard from App so the wizard redirect
 *   happens inside Router context (avoids Navigate-outside-Router invariant violation).
 *   Hub pages consolidate related sub-pages into tabbed/sectioned layouts.
 * SPORT: MASTER-ROUTES.md — all frontend routes
 */

import { Navigate, Route, Routes } from 'react-router-dom'
import { AppLayout } from '../layouts/AppLayout'
import { InboxPage } from '../pages/InboxPage'
import { NotFoundPage } from '../pages/NotFoundPage'
import { WizardLayout } from '../features/onboarding/WizardLayout'
import { TasksPage } from '../pages/TasksPage'
import { ChatPage } from '../features/chat/ChatPage'
import VaultHub from '../pages/hubs/VaultHub'
import ProjectsHub from '../pages/hubs/ProjectsHub'
import AccountsHub from '../pages/hubs/AccountsHub'
import { SettingsHub } from '../pages/hubs/SettingsHub'
import { PersonalPage } from '../pages/PersonalPage'
import { CascadeMetaPage } from '../pages/CascadeMetaPage'

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
 *   Hub routes consolidate former multi-route sections into single tabbed pages.
 * Inputs:  isLoading, launchWizard flags from App (derived from useWizardLaunch).
 * Outputs: <Routes> element with consolidated hub routes + loading / wizard redirect guards.
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

      {/* Root → redirect to chat (E-P9-03: chat is the new home) */}
      <Route path="/" element={<Navigate to="/chat" replace />} />

      {/* Standalone routes — no app chrome */}
      <Route path="/onboarding" element={<WizardLayout />} />

      {/* Authenticated routes — wrapped in persistent app chrome */}
      <Route element={<AppLayout />}>
        {/* E-P9-03: in-app prompt box — the chat page */}
        <Route path="/chat" element={<ChatPage />} />
        {/* Legacy /dashboard redirect to /chat */}
        <Route path="/dashboard" element={<Navigate to="/chat" replace />} />
        <Route path="/inbox" element={<InboxPage />} />
        {/* E-P8-02: Kanban tasks board */}
        <Route path="/tasks" element={<TasksPage />} />

        {/* Hub: vault, graph, memory, personal, instructions consolidated */}
        <Route path="/vault" element={<VaultHub />} />

        {/* Hub: project-map, pews, context, rag-explorer consolidated */}
        <Route path="/projects" element={<ProjectsHub />} />

        {/* Hub: accounts, provider settings, usage consolidated */}
        <Route path="/accounts" element={<AccountsHub />} />
        {/* Backward-compat redirect — provider settings moved into accounts hub */}
        <Route path="/settings/providers" element={<Navigate to="/accounts?tab=providers" replace />} />

        {/* Hub: settings, templates, library, cascade meta consolidated */}
        <Route path="/settings" element={<SettingsHub />} />

        {/* Personal hub — threads from rag-15 personal endpoints */}
        <Route path="/personal" element={<PersonalPage />} />

        {/* Cascade meta — config/tier view (also reachable from /settings?tab=cascade) */}
        <Route path="/settings/cascade" element={<CascadeMetaPage />} />
      </Route>

      {/* Catch-all */}
      <Route path="*" element={<NotFoundPage />} />
    </Routes>
  )
}
