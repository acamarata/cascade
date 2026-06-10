/**
 * Purpose: Settings tab stub for widget position/size configuration.
 *   P3 placeholder — full widget geometry editor deferred to a future release.
 * Inputs:  draft WidgetsSettings (unused in P3 stub).
 * Outputs: <section> with placeholder text and "no widgets configured" message.
 * Constraints:
 *   - No form interactions in P3 — purely informational.
 *   - Spec states: show placeholder "No widgets configured" for P3.
 * SPORT: MASTER-COMPONENTS.md — WidgetsTab — T-P3-E07-16
 */

import type { WidgetsSettings } from '@/types/settings'

export interface WidgetsTabProps {
  draft: WidgetsSettings
}

export function WidgetsTab({ draft }: WidgetsTabProps) {
  const hasWidgets = Object.keys(draft.positions).length > 0

  return (
    <section aria-labelledby="widgets-tab-heading" className="space-y-4">
      <h2 id="widgets-tab-heading" className="text-base font-semibold">
        Widgets
      </h2>
      <p className="text-sm text-muted-foreground">
        Widget configuration available in a future release.
      </p>

      {hasWidgets ? (
        <ul className="space-y-2" aria-label="Configured widgets">
          {Object.entries(draft.positions).map(([slug, geo]) => (
            <li
              key={slug}
              className="rounded-md border border-border px-4 py-2.5 text-sm font-mono text-muted-foreground"
            >
              {slug} — {geo.width}×{geo.height} @ ({geo.x}, {geo.y})
            </li>
          ))}
        </ul>
      ) : (
        <div className="rounded-md border border-border px-4 py-6 text-center text-sm text-muted-foreground">
          No widgets configured.
        </div>
      )}
    </section>
  )
}
