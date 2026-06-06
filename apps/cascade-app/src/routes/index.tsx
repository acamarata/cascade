/**
 * Purpose: Centralised React Router v6 route table for the Cascade app.
 * Inputs:  Consumed by App.tsx inside <Routes>. AppLayout wraps authenticated routes.
 * Outputs: Array of RouteObject plus a RouterApp helper component.
 * Constraints: /onboarding is NOT wrapped in AppLayout (standalone wizard).
 *   Navigate redirect uses replace=true to avoid stacking history entries.
 * SPORT: MASTER-ROUTES.md — all frontend routes
 */

import { Navigate, Route, Routes } from 'react-router-dom';
import { AppLayout } from '../layouts/AppLayout';
import { DashboardPage } from '../pages/DashboardPage';
import { InboxPage } from '../pages/InboxPage';
import { NotFoundPage } from '../pages/NotFoundPage';
import { OnboardingPage } from '../pages/OnboardingPage';
import { SearchPage } from '../pages/SearchPage';
import { SettingsPage } from '../pages/SettingsPage';

/**
 * Purpose: Renders the full route tree.
 *   Authenticated pages are wrapped in AppLayout (chrome).
 *   /onboarding is standalone — no sidebar/topbar.
 * Inputs:  None (reads router context from parent BrowserRouter).
 * Outputs: <Routes> element with 7 routes.
 * Constraints: Must be rendered inside a Router provider.
 * SPORT: MASTER-ROUTES.md
 */
export function RouterApp() {
  return (
    <Routes>
      {/* Root → redirect to dashboard */}
      <Route path="/" element={<Navigate to="/dashboard" replace />} />

      {/* Standalone routes — no app chrome */}
      <Route path="/onboarding" element={<OnboardingPage />} />

      {/* Authenticated routes — wrapped in persistent app chrome */}
      <Route element={<AppLayout />}>
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/inbox" element={<InboxPage />} />
        <Route path="/search" element={<SearchPage />} />
        <Route path="/settings" element={<SettingsPage />} />
      </Route>

      {/* Catch-all */}
      <Route path="*" element={<NotFoundPage />} />
    </Routes>
  );
}
