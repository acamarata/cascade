/**
 * Purpose: /vault/instructions route — makes the merged CASCADE.md instruction cascade
 *   a queryable knowledge object. Lists all six tiers with resolved content, provides
 *   full-text search, and shows a tier diff/inheritance view.
 * Inputs:  None — fetches tier content via cascade_resolve IPC on mount.
 * Outputs: Tabbed page with tier list, search, and diff views.
 * Constraints:
 *   - Loading / empty / error states per tier; a11y landmarks.
 *   - Search is client-side over pre-fetched tier contents (no extra IPC).
 *   - Tier diff view is pure (no I/O after initial fetch).
 *   - Fallback stub when cascade_resolve is not available (dev/Vitest).
 * SPORT: MASTER-ROUTES.md — /vault/instructions (E-P9-02)
 *        MASTER-COMPONENTS.md — InstructionsPage (E-P9-02)
 */

import { useCallback, useEffect, useMemo, useState } from 'react'
import { BookOpen, ChevronDown, ChevronRight, GitMerge, RefreshCw, Search } from 'lucide-react'
import type { TierContent, TierDiff, TierLabel } from '../types/instructions'
import { TIER_DESCRIPTIONS, TIER_LABELS } from '../types/instructions'
import {
  computeTierDiff,
  fetchAllTiers,
  searchInstructions,
} from '../services/instructions/instructionTiers'
import { Button } from '../components/ui/button'

// ── View modes ────────────────────────────────────────────────────────────────

type ViewMode = 'list' | 'search' | 'diff'

// ── Tier badge ────────────────────────────────────────────────────────────────

const TIER_COLORS: Record<TierLabel, string> = {
  GCI: 'bg-violet-500/10 text-violet-700 dark:text-violet-400 border-violet-500/20',
  PCI: 'bg-blue-500/10 text-blue-700 dark:text-blue-400 border-blue-500/20',
  APC: 'bg-cyan-500/10 text-cyan-700 dark:text-cyan-400 border-cyan-500/20',
  PPC: 'bg-green-500/10 text-green-700 dark:text-green-400 border-green-500/20',
  PRC: 'bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/20',
  PAC: 'bg-rose-500/10 text-rose-700 dark:text-rose-400 border-rose-500/20',
}

function TierBadge({ tier, size = 'sm' }: { tier: TierLabel; size?: 'sm' | 'xs' }) {
  return (
    <span
      className={[
        'inline-flex shrink-0 items-center rounded border px-1.5 font-mono font-semibold',
        size === 'xs' ? 'text-[10px] py-0' : 'text-xs py-0.5',
        TIER_COLORS[tier],
      ].join(' ')}
    >
      {tier}
    </span>
  )
}

// ── Tier card (list view) ─────────────────────────────────────────────────────

function TierCard({ tier }: { tier: TierContent }) {
  const [expanded, setExpanded] = useState(false)
  const lineCount = tier.content ? tier.content.split('\n').length : 0

  return (
    <article
      aria-label={`Tier ${tier.tier}`}
      className="rounded-lg border border-border bg-card overflow-hidden"
    >
      {/* Header */}
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-accent/30 transition-colors"
        aria-expanded={expanded}
        aria-controls={`tier-content-${tier.tier}`}
      >
        <TierBadge tier={tier.tier} />
        <span className="flex-1 text-sm font-medium">{TIER_DESCRIPTIONS[tier.tier]}</span>
        {tier.error ? (
          <span className="text-xs text-destructive">Error</span>
        ) : !tier.present ? (
          <span className="text-xs text-muted-foreground">absent</span>
        ) : (
          <span className="text-xs text-muted-foreground">{lineCount} lines</span>
        )}
        {expanded ? (
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        ) : (
          <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        )}
      </button>

      {/* Content */}
      {expanded && (
        <div id={`tier-content-${tier.tier}`} className="border-t border-border bg-muted/20">
          {tier.error ? (
            <p className="px-4 py-3 text-sm text-destructive" role="alert">
              {tier.error}
            </p>
          ) : !tier.present ? (
            <p className="px-4 py-3 text-sm text-muted-foreground italic">
              No CASCADE.md found for this tier.
            </p>
          ) : (
            <pre className="overflow-x-auto px-4 py-3 text-xs text-foreground leading-relaxed whitespace-pre-wrap max-h-80 overflow-y-auto">
              {tier.content}
            </pre>
          )}
        </div>
      )}
    </article>
  )
}

// ── Search view ───────────────────────────────────────────────────────────────

function SearchView({ tiers }: { tiers: TierContent[] }) {
  const [query, setQuery] = useState('')

  const result = useMemo(() => searchInstructions(tiers, query), [tiers, query])

  function highlight(text: string, start: number, end: number): React.ReactNode {
    if (!query || start >= end) return text
    return (
      <>
        {text.slice(0, start)}
        <mark className="bg-yellow-200 dark:bg-yellow-800 rounded-sm px-0.5">
          {text.slice(start, end)}
        </mark>
        {text.slice(end)}
      </>
    )
  }

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Search input */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-4 py-2">
        <Search className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search all instruction tiers…"
          className="flex-1 bg-transparent text-sm text-foreground placeholder:text-muted-foreground focus:outline-none"
          aria-label="Search instructions"
        />
        {result.totalHits > 0 && (
          <span className="text-xs text-muted-foreground" aria-live="polite">
            {result.totalHits} match{result.totalHits !== 1 ? 'es' : ''}
          </span>
        )}
      </div>

      {/* Results */}
      <div className="flex-1 overflow-y-auto" role="list" aria-label="Search results">
        {!query.trim() ? (
          <p className="px-4 py-8 text-center text-sm text-muted-foreground">
            Enter a search term to find content across all instruction tiers.
          </p>
        ) : result.hits.length === 0 ? (
          <p className="px-4 py-8 text-center text-sm text-muted-foreground" aria-live="polite">
            No matches found for &quot;{query}&quot;.
          </p>
        ) : (
          result.hits.map((hit, i) => (
            <div
              key={`${hit.tier}-${hit.lineNo}-${i}`}
              role="listitem"
              className="flex items-start gap-3 border-b border-border px-4 py-2.5 hover:bg-accent/20"
            >
              <TierBadge tier={hit.tier} size="xs" />
              <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                L{hit.lineNo}
              </span>
              <span className="flex-1 text-xs font-mono text-foreground break-all leading-relaxed">
                {highlight(hit.lineText, hit.matchStart, hit.matchEnd)}
              </span>
            </div>
          ))
        )}
      </div>
    </div>
  )
}

// ── Diff view ─────────────────────────────────────────────────────────────────

function DiffView({ tiers }: { tiers: TierContent[] }) {
  const [selectedIdx, setSelectedIdx] = useState(0)

  const diff: TierDiff = useMemo(() => computeTierDiff(tiers, selectedIdx), [tiers, selectedIdx])

  return (
    <div className="flex h-full overflow-hidden">
      {/* Tier selector */}
      <nav
        aria-label="Select tier for diff"
        className="flex w-48 shrink-0 flex-col border-r border-border bg-card"
      >
        {TIER_LABELS.map((label, idx) => {
          const t = tiers.find((x) => x.tier === label)
          return (
            <button
              key={label}
              type="button"
              onClick={() => setSelectedIdx(idx)}
              className={[
                'flex items-center gap-2 px-3 py-2.5 text-left text-xs transition-colors',
                selectedIdx === idx
                  ? 'bg-primary/10 text-primary font-medium'
                  : 'text-muted-foreground hover:bg-accent/30 hover:text-foreground',
              ].join(' ')}
              aria-pressed={selectedIdx === idx}
            >
              <TierBadge tier={label} size="xs" />
              {!t?.present && <span className="ml-auto text-muted-foreground/50">–</span>}
            </button>
          )
        })}
      </nav>

      {/* Diff content */}
      <div className="flex flex-1 flex-col overflow-hidden">
        <div className="flex shrink-0 items-center gap-3 border-b border-border px-4 py-2">
          <TierBadge tier={diff.tier} />
          <span className="text-xs text-muted-foreground">
            {diff.newLineCount} new lines · {diff.inheritedLineCount} inherited from prior tiers
          </span>
        </div>
        <div
          className="flex-1 overflow-y-auto font-mono text-xs leading-relaxed"
          role="region"
          aria-label="Tier diff"
        >
          {diff.lines.length === 0 ? (
            <p className="px-4 py-8 text-center text-muted-foreground">
              {tiers.find((t) => t.tier === diff.tier)?.present
                ? 'No content in this tier.'
                : 'This tier has no CASCADE.md.'}
            </p>
          ) : (
            diff.lines.map((line, i) => (
              <div
                key={i}
                className={[
                  'flex items-start px-4 py-0.5',
                  line.isNew ? 'bg-green-500/10' : '',
                ].join(' ')}
              >
                <span
                  className={[
                    'mr-3 shrink-0 select-none text-muted-foreground/60',
                    line.isNew ? '!text-green-600 dark:!text-green-400' : '',
                  ].join(' ')}
                  aria-hidden="true"
                >
                  {line.isNew ? '+' : ' '}
                </span>
                <span
                  className={line.isNew ? 'text-green-800 dark:text-green-200' : 'text-foreground'}
                >
                  {line.text || ' '}
                </span>
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export function InstructionsPage() {
  const [tiers, setTiers] = useState<TierContent[]>([])
  const [loading, setLoading] = useState(false)
  const [globalError, setGlobalError] = useState<string | null>(null)
  const [viewMode, setViewMode] = useState<ViewMode>('list')

  const load = useCallback(async () => {
    setLoading(true)
    setGlobalError(null)
    try {
      const data = await fetchAllTiers()
      setTiers(data)
    } catch (e) {
      setGlobalError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const presentCount = tiers.filter((t) => t.present).length

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Page header */}
      <header className="flex shrink-0 items-center gap-3 border-b border-border px-6 py-3">
        <BookOpen className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-lg font-semibold">Instructions</h1>
        <span className="text-sm text-muted-foreground">CASCADE.md across 6 tiers</span>
        {!loading && tiers.length > 0 && (
          <span className="ml-1 text-sm text-muted-foreground">
            · {presentCount}/{TIER_LABELS.length} tiers resolved
          </span>
        )}
        <div className="ml-auto flex items-center gap-2">
          {/* View mode tabs */}
          {(['list', 'search', 'diff'] as ViewMode[]).map((mode) => (
            <button
              key={mode}
              type="button"
              onClick={() => setViewMode(mode)}
              aria-pressed={viewMode === mode}
              className={[
                'rounded px-3 py-1 text-xs font-medium transition-colors',
                viewMode === mode
                  ? 'bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
              ].join(' ')}
            >
              {mode === 'list' && 'List'}
              {mode === 'search' && (
                <span className="flex items-center gap-1">
                  <Search className="h-3 w-3" aria-hidden="true" />
                  Search
                </span>
              )}
              {mode === 'diff' && (
                <span className="flex items-center gap-1">
                  <GitMerge className="h-3 w-3" aria-hidden="true" />
                  Diff
                </span>
              )}
            </button>
          ))}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void load()}
            disabled={loading}
            aria-label="Reload instruction tiers"
            className="h-7 px-2"
          >
            <RefreshCw
              className={['h-3.5 w-3.5', loading ? 'animate-spin' : ''].join(' ')}
              aria-hidden="true"
            />
          </Button>
        </div>
      </header>

      {/* Body */}
      <div className="flex-1 overflow-hidden">
        {loading && tiers.length === 0 ? (
          <div
            className="flex h-full items-center justify-center text-sm text-muted-foreground"
            aria-live="polite"
          >
            Loading instruction tiers…
          </div>
        ) : globalError ? (
          <div className="flex h-full items-center justify-center p-6" role="alert">
            <p className="max-w-md text-center text-sm text-destructive">{globalError}</p>
          </div>
        ) : viewMode === 'list' ? (
          <div className="h-full overflow-y-auto p-4">
            <div className="flex flex-col gap-3 max-w-3xl mx-auto">
              {TIER_LABELS.map((label) => {
                const tier = tiers.find((t) => t.tier === label)
                if (!tier) return null
                return <TierCard key={label} tier={tier} />
              })}
            </div>
          </div>
        ) : viewMode === 'search' ? (
          <SearchView tiers={tiers} />
        ) : (
          <DiffView tiers={tiers} />
        )}
      </div>
    </div>
  )
}
