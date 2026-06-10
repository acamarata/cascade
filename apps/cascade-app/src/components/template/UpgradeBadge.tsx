/**
 * Purpose: Amber badge overlaid on a TemplateCard when a newer version of an applied
 *   template is available in the registry.
 * Inputs:  version — the new version string to display (e.g. "1.2.0").
 * Outputs: Inline badge element with "Upgrade to vX.Y.Z" label.
 * Constraints:
 *   - Uses amber colour tokens; visible at WCAG 4.5:1 contrast minimum.
 *   - aria-label for screen readers.
 *   - No Radix dependency — simple inline element.
 * SPORT: MASTER-COMPONENTS.md — UpgradeBadge
 */

import React from 'react'
import { ArrowUp } from 'lucide-react'

interface UpgradeBadgeProps {
  /** The new version string (e.g. "1.2.0"). Displayed as "Upgrade to v{version}". */
  version: string
}

/**
 * UpgradeBadge — amber pill indicating a newer template version is available.
 *
 * Rendered inside a TemplateCard when the template has been applied to the current
 * target and a newer version exists in the registry.
 */
export function UpgradeBadge({ version }: UpgradeBadgeProps): React.ReactElement {
  return (
    <span
      role="status"
      aria-label={`Upgrade available: version ${version}`}
      className="inline-flex items-center gap-0.5 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold text-amber-800 dark:bg-amber-900/40 dark:text-amber-300"
    >
      <ArrowUp className="h-2.5 w-2.5" aria-hidden="true" />v{version}
    </span>
  )
}
