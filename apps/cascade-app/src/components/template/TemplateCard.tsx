/**
 * Purpose: Individual template card rendered in the TemplateBrowser grid.
 *   Displays id, version badge, tier badge, and truncated description.
 *   When upgradeable=true and upgradeToVersion is provided, renders an UpgradeBadge.
 * Inputs:  TemplateEntryIpc entry, isSelected flag, onClick callback,
 *          optional upgradeable/upgradeToVersion props.
 * Outputs: Keyboard-navigable card with aria-selected and role=gridcell.
 * Constraints:
 *   - Description truncated to 2 lines (line-clamp-2).
 *   - Highlighted when isSelected=true.
 *   - Keyboard: Enter/Space triggers onClick; arrow nav handled by parent grid.
 * SPORT: MASTER-COMPONENTS.md — TemplateCard
 */

import React from 'react'
import type { TemplateEntryIpc } from '../../types/templates'
import { UpgradeBadge } from './UpgradeBadge'

interface TemplateCardProps {
  entry: TemplateEntryIpc
  isSelected: boolean
  onClick: () => void
  /** Whether a newer version is available for an applied template. */
  upgradeable?: boolean
  /** The new version string to display in the UpgradeBadge (required when upgradeable). */
  upgradeToVersion?: string
}

/** Tier → badge color mapping. */
const TIER_COLORS: Record<string, string> = {
  gci: 'bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300',
  pci: 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300',
  apc: 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300',
  ppc: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/40 dark:text-yellow-300',
  prc: 'bg-orange-100 text-orange-800 dark:bg-orange-900/40 dark:text-orange-300',
  pac: 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300',
  any: 'bg-muted text-muted-foreground',
}

/**
 * Template grid card.
 */
export function TemplateCard({
  entry,
  isSelected,
  onClick,
  upgradeable = false,
  upgradeToVersion,
}: TemplateCardProps): React.ReactElement {
  const tierColor = TIER_COLORS[entry.tier] ?? TIER_COLORS['any']

  function handleKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onClick()
    }
  }

  return (
    <div
      role="gridcell"
      tabIndex={0}
      aria-selected={isSelected}
      onClick={onClick}
      onKeyDown={handleKeyDown}
      className={[
        'cursor-pointer rounded-lg border p-3 transition-colors focus:outline-none focus:ring-2 focus:ring-ring',
        isSelected
          ? 'border-primary bg-primary/5'
          : 'border-border bg-card hover:border-primary/40 hover:bg-accent/20',
      ].join(' ')}
    >
      {/* Header: id + badges */}
      <div className="mb-1.5 flex items-start justify-between gap-2">
        <h3 className="truncate text-sm font-medium leading-tight text-foreground">{entry.id}</h3>
        <div className="flex shrink-0 items-center gap-1">
          <span
            className={['inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide', tierColor].join(' ')}
            aria-label={`Tier: ${entry.tier}`}
          >
            {entry.tier}
          </span>
          <span
            className="inline-flex items-center rounded bg-muted px-1.5 py-0.5 text-[10px] font-mono text-muted-foreground"
            aria-label={`Version ${entry.version}`}
          >
            v{entry.version}
          </span>
          {upgradeable && upgradeToVersion && (
            <UpgradeBadge version={upgradeToVersion} />
          )}
        </div>
      </div>

      {/* Description (2-line clamp) */}
      <p className="line-clamp-2 text-xs text-muted-foreground">{entry.description}</p>
    </div>
  )
}
