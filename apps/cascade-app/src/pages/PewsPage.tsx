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

/**
 * Purpose: Route entry-point for the PEWS tree page.
 * Inputs:  None.
 * Outputs: Full-height panel mounting PewsTreePanel; project root defaults to
 *   the user's home directory (resolved at runtime).
 */
export function PewsPage() {
  return (
    <div className="h-full overflow-hidden" data-page="pews">
      <PewsTreePanel />
    </div>
  )
}
