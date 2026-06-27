/**
 * Purpose: Composes the Fleet tab in AccountsHub — FleetWidget (quota gauges) on top,
 *   FleetQuotaPanel (per-account bars + countdown) below.
 * Inputs:  None — both child components fetch their own data.
 * Outputs: Two-section layout inside a scrollable container.
 * Constraints:
 *   - Thin wrapper only; no data fetching here.
 *   - FleetWidget is no longer orphaned after this is wired in AccountsHub.
 * SPORT: MASTER-COMPONENTS.md — FleetTabContent (fleet-02)
 */

import { FleetWidget } from '../../components/widget/FleetWidget'
import { FleetQuotaPanel } from '../../components/fleet/FleetQuotaPanel'

export function FleetTabContent() {
  return (
    <div className="flex flex-col gap-6 p-4 overflow-y-auto h-full">
      <section aria-label="Fleet quota gauges">
        <FleetWidget />
      </section>

      <section aria-label="Per-account quota status">
        <FleetQuotaPanel />
      </section>
    </div>
  )
}
