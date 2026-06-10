/**
 * Purpose: Compact template picker for the onboarding wizard Phase 4 (merge scaffold step).
 *   Renders a search input + 3 filter chips (tier/stack/shape) + a scrollable multi-select
 *   card list. Used as the pre-AI-merge template selection step.
 *
 * Inputs:
 *   - entries: TemplateEntryIpc[] — catalog loaded from template_list IPC.
 *   - selectedIds: string[] — currently selected template ids (controlled).
 *   - onChange: (ids: string[]) => void — emitted on every selection change.
 *   - isLoading: boolean — shows skeleton while catalog loads (optional, default false).
 *   - loadError: string | null — shows inline error if catalog failed (optional).
 *
 * Outputs:
 *   - Multi-select card list (TemplateCard with checkbox overlay).
 *   - onChange fires with updated id list on every toggle.
 *
 * Constraints:
 *   - max-height 300px on the card list area; scrollable.
 *   - Reuses TemplateCard with a checkbox overlay variant (isSelected drives checked state).
 *   - Filter chips: tier select + stack select + search input (3 controls, compact row).
 *   - Empty state: "No templates match" message.
 *   - Graceful when entries is empty: renders "No templates available".
 *   - No duplicate logic — filtering delegated from existing catalog data, not re-fetched.
 *
 * SPORT: MASTER-COMPONENTS.md — TemplatePickerCompact — wizard Phase 4 pre-scaffold — T-P3-E05-19
 */

import React, { useMemo, useState } from 'react'
import { Search } from 'lucide-react'
import type { TemplateEntryIpc, TemplateTier } from '../../types/templates'
import { TemplateCard } from './TemplateCard'

// ── Filter state ──────────────────────────────────────────────────────────────

interface CompactFilter {
  search: string
  tier: TemplateTier | ''
  stack: string
}

const INITIAL_FILTER: CompactFilter = { search: '', tier: '', stack: '' }

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

// ── Props ─────────────────────────────────────────────────────────────────────

export interface TemplatePickerCompactProps {
  /** Full catalog from template_list IPC. */
  entries: TemplateEntryIpc[]
  /** Controlled selected ids. */
  selectedIds: string[]
  /** Fired on every selection change. */
  onChange: (ids: string[]) => void
  /** Skeleton mode while catalog loads. */
  isLoading?: boolean
  /** Inline error message if catalog failed to load. */
  loadError?: string | null
}

// ── Component ─────────────────────────────────────────────────────────────────

/**
 * Compact multi-select template picker for the wizard merge scaffold step.
 *
 * @example
 * ```tsx
 * <TemplatePickerCompact
 *   entries={catalogEntries}
 *   selectedIds={selectedTemplates}
 *   onChange={setSelectedTemplates}
 * />
 * ```
 */
export function TemplatePickerCompact({
  entries,
  selectedIds,
  onChange,
  isLoading = false,
  loadError = null,
}: TemplatePickerCompactProps): React.ReactElement {
  const [filter, setFilter] = useState<CompactFilter>(INITIAL_FILTER)

  // Derive unique stacks from catalog for the stack chip
  const availableStacks = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const e of entries) {
      for (const s of e.stacks) set.add(s)
    }
    return Array.from(set).sort()
  }, [entries])

  // Apply filters (AND logic matching TemplateBrowser's pattern)
  const filtered = useMemo<TemplateEntryIpc[]>(() => {
    return entries.filter((e) => {
      if (filter.tier && e.tier !== filter.tier) return false
      if (filter.stack && !e.stacks.includes(filter.stack)) return false
      if (filter.search) {
        const q = filter.search.toLowerCase()
        if (
          !e.id.toLowerCase().includes(q) &&
          !e.description.toLowerCase().includes(q)
        ) {
          return false
        }
      }
      return true
    })
  }, [entries, filter])

  // Toggle a template id in/out of selectedIds
  function handleToggle(id: string) {
    const next = selectedIds.includes(id)
      ? selectedIds.filter((s) => s !== id)
      : [...selectedIds, id]
    onChange(next)
  }

  // ── Loading state ──────────────────────────────────────────────────────────

  if (isLoading) {
    return (
      <div
        aria-busy="true"
        aria-label="Loading templates"
        className="flex flex-col gap-2"
      >
        {[1, 2, 3].map((i) => (
          <div
            key={i}
            className="h-16 animate-pulse rounded-lg border border-border bg-muted/40"
            aria-hidden="true"
          />
        ))}
      </div>
    )
  }

  // ── Error state ────────────────────────────────────────────────────────────

  if (loadError) {
    return (
      <div
        role="alert"
        className="rounded-md border border-destructive/40 bg-destructive/10 px-4 py-3 text-sm text-destructive"
      >
        <span className="font-medium">Could not load templates:</span>{' '}
        {loadError}
      </div>
    )
  }

  // ── Empty catalog ──────────────────────────────────────────────────────────

  if (entries.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No templates available. You can proceed without a template.
      </p>
    )
  }

  // ── Main render ────────────────────────────────────────────────────────────

  return (
    <div className="flex flex-col gap-2" data-testid="template-picker-compact">
      {/* Filter row: search + tier chip + stack chip */}
      <div
        role="search"
        aria-label="Filter templates"
        className="flex flex-wrap items-center gap-2"
      >
        {/* Search */}
        <div className="relative min-w-0 flex-1">
          <Search
            className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <input
            id="tpc-search"
            type="search"
            value={filter.search}
            onChange={(e) => setFilter((f) => ({ ...f, search: e.target.value }))}
            placeholder="Search templates…"
            aria-label="Search templates by name or description"
            className="h-8 w-full rounded-md border border-input bg-background pl-8 pr-3 text-xs placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring"
          />
        </div>

        {/* Tier chip */}
        <select
          id="tpc-tier"
          value={filter.tier}
          onChange={(e) =>
            setFilter((f) => ({ ...f, tier: e.target.value as TemplateTier | '' }))
          }
          aria-label="Filter by tier"
          className="h-8 rounded-md border border-input bg-background px-2 text-xs focus:outline-none focus:ring-2 focus:ring-ring"
        >
          {TIER_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>

        {/* Stack chip (only when stacks are available) */}
        {availableStacks.length > 0 && (
          <select
            id="tpc-stack"
            value={filter.stack}
            onChange={(e) =>
              setFilter((f) => ({ ...f, stack: e.target.value }))
            }
            aria-label="Filter by stack tag"
            className="h-8 rounded-md border border-input bg-background px-2 text-xs focus:outline-none focus:ring-2 focus:ring-ring"
          >
            <option value="">All stacks</option>
            {availableStacks.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        )}
      </div>

      {/* Selection summary badge */}
      {selectedIds.length > 0 && (
        <p className="text-xs text-muted-foreground" aria-live="polite" aria-atomic="true">
          {selectedIds.length} template{selectedIds.length !== 1 ? 's' : ''} selected
        </p>
      )}

      {/* Scrollable card list */}
      <div
        role="group"
        aria-label="Template list — multi-select"
        className="flex flex-col gap-1.5 overflow-y-auto"
        style={{ maxHeight: '300px' }}
      >
        {filtered.length === 0 ? (
          <p className="py-4 text-center text-xs text-muted-foreground">
            No templates match your filters.
          </p>
        ) : (
          filtered.map((entry) => (
            <div key={entry.id} className="relative">
              {/* Checkbox overlay — appears top-left over TemplateCard */}
              <label
                htmlFor={`tpc-check-${entry.id}`}
                className="sr-only"
              >
                {`Select ${entry.id}`}
              </label>
              <input
                id={`tpc-check-${entry.id}`}
                type="checkbox"
                checked={selectedIds.includes(entry.id)}
                onChange={() => handleToggle(entry.id)}
                aria-label={`Select template ${entry.id}`}
                className="absolute left-2 top-2 z-10 h-4 w-4 cursor-pointer accent-primary"
              />
              {/* Indent the card to leave room for the checkbox */}
              <div className="pl-7">
                <TemplateCard
                  entry={entry}
                  isSelected={selectedIds.includes(entry.id)}
                  onClick={() => handleToggle(entry.id)}
                />
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
