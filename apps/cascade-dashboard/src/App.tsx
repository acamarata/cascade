/**
 * Purpose: Root router configuration — defines the three top-level section routes.
 * Inputs: none.
 * Outputs: React Router route tree with DashboardLayout as shell.
 * Constraints: Index route redirects to /global so no blank page on root navigation.
 * SPORT: T-P3-E02-04 layout shell
 */
import { Routes, Route, Navigate } from 'react-router-dom'
import { DashboardLayout } from '@/layouts/DashboardLayout'
import { GlobalSection } from '@/routes/GlobalSection'
import { PersonalSection } from '@/routes/PersonalSection'
import { ProjectsSection } from '@/routes/ProjectsSection'

export function App() {
  return (
    <Routes>
      <Route path="/" element={<DashboardLayout />}>
        <Route index element={<Navigate to="/global" replace />} />
        <Route path="global" element={<GlobalSection />} />
        <Route path="personal" element={<PersonalSection />} />
        <Route path="projects" element={<ProjectsSection />} />
      </Route>
    </Routes>
  )
}
