/**
 * Purpose: Cascade Tiers tab — renders the six-tier cascade chain as a vertical
 *   CSS tree. Existing tiers (exists:true) shown in green; missing tiers in
 *   gray with a "Create" button that generates the .cascade/ stub.
 * Inputs:  projectRoot — absolute path to inspect (from props/selector).
 *          get_cascade_tier_tree IPC via getCascadeTierTree(root).
 * Outputs: Ordered list of 6 tier rows with status indicators.
 * Constraints:
 *   - Auto-refreshes on focus.
 *   - No graph lib — pure CSS vertical list.
 *   - "Create" opens the path in Finder (file creation is not yet wired in P3).
 * SPORT: MASTER-COMPONENTS.md — CascadeTierTree (T-P3-E07-13)
 */

import { useCallback, useEffect, useState } from 'react'
import { CheckCircle2, Circle, FolderPlus, Loader2 } from 'lucide-react'
import { open } from '@tauri-apps/plugin-shell'
import { getCascadeTierTree } from '../../types/maps'
import type { TierEntry } from '../../types/maps'
import { tierExists, tierLabel } from './mapTransforms'

interface CascadeTierTreeProps {
  /** Absolute path of the project root to inspect. */
  projectRoot: string
}

const TIER_DESCRIPTIONS: Record<string, string> = {
  GCI: 'Global Cascade Instructions — ~/.cascade/CASCADE.md',
  PCI: 'Platform Cascade Instructions — parent-scope',
  APC: 'Account / Persona Cascade Instructions',
  PPC: 'Project-parent Cascade Instructions',
  PRC: 'Project Cascade Instructions',
  PAC: 'Per-app Cascade Instructions',
}

function TierRow({ entry }: { entry: TierEntry }) {
  const exists = tierExists(entry)

  function handleCreate() {
    // Open the parent directory; actual file creation is deferred to Settings (E-07 W-05).
    const dir = entry.path.replace(/\/[^/]+$/, '')
    open(dir).catch(() => {/* ignore */})
  }

  return (
    <li
      className="flex items-start gap-3 rounded-md border border-border bg-card/50 p-3 transition-colors hover:bg-card"
      aria-label={`${entry.tier} tier — ${exists ? 'present' : 'missing'}`}
    >
      {/* Status icon */}
      {exists ? (
        <CheckCircle2
          className="mt-0.5 h-4 w-4 shrink-0 text-green-500"
          aria-hidden="true"
        />
      ) : (
        <Circle
          className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground/40"
          aria-hidden="true"
        />
      )}

      {/* Content */}
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span
            className={`text-xs font-semibold tabular-nums ${
              exists ? 'text-green-400' : 'text-muted-foreground'
            }`}
          >
            {entry.tier}
          </span>
          <span className="truncate text-xs text-muted-foreground">
            {entry.name}
          </span>
        </div>
        <p className="mt-0.5 truncate text-[10px] text-muted-foreground/60">
          {TIER_DESCRIPTIONS[entry.tier] ?? entry.path}
        </p>
        {exists && (
          <p className="mt-0.5 truncate text-[10px] text-green-600/70" aria-label="Path">
            {entry.path}
          </p>
        )}
      </div>

      {/* Create button for missing tiers */}
      {!exists && (
        <button
          type="button"
          onClick={handleCreate}
          className="ml-auto flex shrink-0 items-center gap-1.5 rounded border border-border px-2 py-1 text-[10px] text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
          aria-label={`Create ${entry.tier} at ${entry.path}`}
        >
          <FolderPlus className="h-3 w-3" aria-hidden="true" />
          Create
        </button>
      )}
    </li>
  )
}

export function CascadeTierTree({ projectRoot }: CascadeTierTreeProps) {
  const [tiers, setTiers] = useState<TierEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const loadData = useCallback(async () => {
    try {
      setLoading(true)
      setError(null)
      const data = await getCascadeTierTree(projectRoot)
      setTiers(data)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [projectRoot])

  useEffect(() => {
    void loadData()
  }, [loadData])

  useEffect(() => {
    const handler = () => void loadData()
    window.addEventListener('focus', handler)
    return () => window.removeEventListener('focus', handler)
  }, [loadData])

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center" role="status" aria-live="polite">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" aria-hidden="true" />
        <span className="ml-2 text-sm text-muted-foreground">Loading cascade tiers…</span>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3" role="alert">
        <p className="text-sm text-destructive">Failed to load cascade tiers</p>
        <p className="max-w-xs text-center text-xs text-muted-foreground">{error}</p>
        <button
          type="button"
          onClick={() => void loadData()}
          className="rounded border border-border px-3 py-1.5 text-xs hover:bg-accent/50"
        >
          Retry
        </button>
      </div>
    )
  }

  const existingCount = tiers.filter(tierExists).length
  const totalCount = tiers.length

  return (
    <div className="flex h-full flex-col overflow-hidden p-4" data-testid="cascade-tier-tree">
      {/* Header summary */}
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-sm font-semibold text-foreground">Cascade tier chain</h3>
        <span className="rounded-full bg-muted px-2 py-0.5 text-[10px] text-muted-foreground">
          {existingCount}/{totalCount} present
        </span>
      </div>

      {/* Legend */}
      <div className="mb-3 flex items-center gap-4">
        <span className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
          <CheckCircle2 className="h-3.5 w-3.5 text-green-500" aria-hidden="true" />
          Present
        </span>
        <span className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
          <Circle className="h-3.5 w-3.5 text-muted-foreground/40" aria-hidden="true" />
          Missing
        </span>
      </div>

      {/* Tier list */}
      <ol
        className="flex flex-col gap-2 overflow-y-auto"
        aria-label="Cascade tier chain, outermost to innermost"
      >
        {tiers.map((entry) => (
          <TierRow key={entry.tier} entry={entry} />
        ))}
      </ol>

      {tiers.length === 0 && (
        <p className="mt-8 text-center text-sm text-muted-foreground">
          No tier data available. Select a project root first.
        </p>
      )}
    </div>
  )
}

// Re-export helper for tests
export { tierLabel }
