/**
 * Purpose: Filter bar for the TemplateBrowser page.
 *   Provides a search input and tier/stack/shape filter chips.
 * Inputs:  onFilterChange callback, current filter values.
 * Outputs: Controlled filter bar emitting TemplateFilter on every change (realtime, AND logic).
 * Constraints:
 *   - All filters combine with AND logic — no submit button needed.
 *   - Accessible: label per input, aria-label on chip buttons.
 * SPORT: MASTER-COMPONENTS.md — TemplateFilterBar
 */

import React from 'react'
import { Search, X } from 'lucide-react'
import type { TemplateTier } from '../../types/templates'

export interface TemplateFilter {
  search: string
  tier: TemplateTier | ''
  stack: string
  shape: string
}

interface TemplateFilterBarProps {
  filter: TemplateFilter
  onFilterChange: (filter: TemplateFilter) => void
  /** Available stack tags derived from the loaded catalog. */
  availableStacks: string[]
  /** Available shape tags derived from the loaded catalog. */
  availableShapes: string[]
}

const TIER_OPTIONS: Array<{ value: TemplateTier | ''; label: string }> = [
  { value: '', label: 'All tiers' },
  { value: 'gci', label: 'GCI' },
  { value: 'pci', label: 'PCI' },
  { value: 'apc', label: 'APC' },
  { value: 'ppc', label: 'PPC' },
  { value: 'prc', label: 'PRC' },
  { value: 'pac', label: 'PAC' },
  { value: 'any', label: 'Any' },
]

/**
 * Filter bar for TemplateBrowser: search + tier/stack/shape selectors.
 * Calls onFilterChange on every keystroke/change — no submit needed.
 */
export function TemplateFilterBar({
  filter,
  onFilterChange,
  availableStacks,
  availableShapes,
}: TemplateFilterBarProps): React.ReactElement {
  function update(partial: Partial<TemplateFilter>) {
    onFilterChange({ ...filter, ...partial })
  }

  function clearAll() {
    onFilterChange({ search: '', tier: '', stack: '', shape: '' })
  }

  const hasActiveFilters =
    filter.search !== '' || filter.tier !== '' || filter.stack !== '' || filter.shape !== ''

  return (
    <div
      role="search"
      aria-label="Filter templates"
      className="flex flex-wrap items-center gap-2 border-b border-border bg-card px-4 py-3"
    >
      {/* Search input */}
      <div className="relative flex-1 min-w-48">
        <Search
          className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
          aria-hidden="true"
        />
        <input
          id="template-search"
          type="search"
          value={filter.search}
          onChange={(e) => update({ search: e.target.value })}
          placeholder="Search templates…"
          aria-label="Search templates by name or description"
          className="h-9 w-full rounded-md border border-input bg-background pl-8 pr-3 text-sm placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring"
        />
      </div>

      {/* Tier selector */}
      <div className="flex items-center gap-1.5">
        <label htmlFor="template-tier-filter" className="text-xs text-muted-foreground">
          Tier
        </label>
        <select
          id="template-tier-filter"
          value={filter.tier}
          onChange={(e) => update({ tier: e.target.value as TemplateTier | '' })}
          aria-label="Filter by tier"
          className="h-9 rounded-md border border-input bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
        >
          {TIER_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </div>

      {/* Stack selector */}
      {availableStacks.length > 0 && (
        <div className="flex items-center gap-1.5">
          <label htmlFor="template-stack-filter" className="text-xs text-muted-foreground">
            Stack
          </label>
          <select
            id="template-stack-filter"
            value={filter.stack}
            onChange={(e) => update({ stack: e.target.value })}
            aria-label="Filter by stack tag"
            className="h-9 rounded-md border border-input bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
          >
            <option value="">All stacks</option>
            {availableStacks.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </div>
      )}

      {/* Shape selector */}
      {availableShapes.length > 0 && (
        <div className="flex items-center gap-1.5">
          <label htmlFor="template-shape-filter" className="text-xs text-muted-foreground">
            Shape
          </label>
          <select
            id="template-shape-filter"
            value={filter.shape}
            onChange={(e) => update({ shape: e.target.value })}
            aria-label="Filter by project shape"
            className="h-9 rounded-md border border-input bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
          >
            <option value="">All shapes</option>
            {availableShapes.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </div>
      )}

      {/* Clear all */}
      {hasActiveFilters && (
        <button
          type="button"
          onClick={clearAll}
          aria-label="Clear all filters"
          className="flex h-9 items-center gap-1 rounded-md px-2 text-xs text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
        >
          <X className="h-3 w-3" aria-hidden="true" />
          Clear
        </button>
      )}
    </div>
  )
}
