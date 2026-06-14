/**
 * PewsPage.tsx — Route wrapper for the PEWS mutation UI.
 *
 * Purpose: Thin page-level wrapper that renders PewsTreePanel inside the app
 *   layout. Passes the default project root (cascade repo). The panel itself
 *   exposes a root selector, so the user can switch projects inline.
 *
 * Inputs:  None (route-level; no props).
 * Outputs: Full-height PewsTreePanel.
 * Constraints: h-full — inherits full layout height from AppLayout.
 * SPORT: MASTER-ROUTES.md — /pews (E-P8-05)
 */

import { PewsTreePanel } from '../features/pews/PewsTreePanel'

const DEFAULT_ROOT = '/Volumes/X9/Sites/acamarata/cascade'

/**
 * Purpose: Route entry-point for the PEWS tree page.
 * Inputs:  None.
 * Outputs: Full-height panel mounting PewsTreePanel for the cascade project root.
 */
export function PewsPage() {
  return (
    <div className="h-full overflow-hidden" data-page="pews">
      <PewsTreePanel initialPhaseRoot={DEFAULT_ROOT} />
    </div>
  )
}
