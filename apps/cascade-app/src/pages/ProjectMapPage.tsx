/**
 * Purpose: /project-map route page — thin wrapper for ProjectMapPanel.
 * Inputs:  None.
 * Outputs: Full-height ProjectMapPanel.
 * Constraints: No extra chrome — AppLayout already provides sidebar/topbar.
 * SPORT: MASTER-ROUTES.md — /project-map (T-P3-E07-13)
 */

import { ProjectMapPanel } from '../features/project-map/ProjectMapPanel'

export function ProjectMapPage() {
  return <ProjectMapPanel />
}
