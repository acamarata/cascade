/**
 * Purpose: Root router configuration — defines the three top-level section routes.
 * Inputs: none.
 * Outputs: React Router route tree with DashboardLayout as shell.
 * Constraints: Index route redirects to /global so no blank page on root navigation.
 *   Section components are lazy-loaded (T-P7-E17-02) so the initial bundle only
 *   ships the shell + the active section. DashboardLayout stays eager — it is the
 *   persistent chrome (sidebar/header) and must not suspend on every navigation.
 * SPORT: T-P3-E02-04 layout shell
 */
import { lazy, Suspense } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import { DashboardLayout } from '@/layouts/DashboardLayout'

const GlobalSection = lazy(() =>
  import('@/routes/GlobalSection').then(m => ({ default: m.GlobalSection }))
)
const PersonalSection = lazy(() =>
  import('@/routes/PersonalSection').then(m => ({ default: m.PersonalSection }))
)
const ProjectsSection = lazy(() =>
  import('@/routes/ProjectsSection').then(m => ({ default: m.ProjectsSection }))
)

export function App() {
  return (
    <Suspense
      fallback={
        <div className="flex items-center justify-center h-screen bg-background">
          <div className="text-lg text-muted-foreground">Loading...</div>
        </div>
      }
    >
      <Routes>
        <Route path="/" element={<DashboardLayout />}>
          <Route index element={<Navigate to="/global" replace />} />
          <Route path="global" element={<GlobalSection />} />
          <Route path="personal" element={<PersonalSection />} />
          <Route path="projects" element={<ProjectsSection />} />
        </Route>
      </Routes>
    </Suspense>
  )
}
