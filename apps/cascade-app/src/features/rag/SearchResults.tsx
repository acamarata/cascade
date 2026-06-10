/**
 * Purpose: Renders a ranked list of RAG Citation cards.
 *   Each card shows: source path (open-in-vault seam), heading_path badge,
 *   snippet preview (monospace pre), and rrf_score badge.
 * Inputs:
 *   citations  — array of RagCitation (up to 10).
 *   loading    — true while query is in-flight.
 *   error      — error string or null.
 *   queryMs    — optional query latency in ms.
 *   onOpenFile — optional callback invoked with path on citation click (vault seam).
 * Outputs: <section role="list"> or empty/loading/error state.
 * Constraints:
 *   - role="list" on container; role="listitem" on each card (CR-C requirement).
 *   - Score displayed with 3 decimal places.
 *   - Does not crash when citations = [].
 * SPORT: MASTER-COMPONENTS.md — SearchResults (T-P4-E01-27)
 */

import { ExternalLink, Loader2 } from 'lucide-react'
import type { RagCitation } from './types'

// ── Citation card ─────────────────────────────────────────────────────────────

interface CitationCardProps {
  citation: RagCitation
  rank: number
  onOpenFile?: (path: string) => void
}

function CitationCard({ citation, rank, onOpenFile }: CitationCardProps) {
  const basename = citation.path.split('/').pop() ?? citation.path

  return (
    <article
      role="listitem"
      className="rounded-lg border border-border bg-card p-4 text-sm transition-colors hover:bg-muted/30"
      data-testid="citation-card"
    >
      {/* Header row: rank + path + score */}
      <div className="mb-2 flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary"
            aria-label={`Rank ${rank}`}
          >
            {rank}
          </span>

          <button
            type="button"
            onClick={() => onOpenFile?.(citation.path)}
            title={citation.path}
            aria-label={onOpenFile ? `Open ${basename} in vault` : undefined}
            className="flex min-w-0 items-center gap-1 truncate text-left font-mono text-xs text-foreground hover:text-primary hover:underline focus:outline-none focus:ring-1 focus:ring-ring"
          >
            <span className="truncate">{basename}</span>
            {onOpenFile && (
              <ExternalLink className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
            )}
          </button>
        </div>

        {/* RRF score badge */}
        <span
          className="shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-xs tabular-nums text-muted-foreground"
          title="Reciprocal-rank fusion score"
          aria-label={`Score ${citation.rrf_score.toFixed(3)}`}
        >
          {citation.rrf_score.toFixed(3)}
        </span>
      </div>

      {/* Heading path badge */}
      {citation.heading_path && (
        <div className="mb-2">
          <span className="inline-flex items-center rounded border border-border bg-muted/50 px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
            {citation.heading_path}
          </span>
        </div>
      )}

      {/* Snippet */}
      <pre
        className="overflow-x-auto whitespace-pre-wrap break-words rounded bg-muted/40 p-2 font-mono text-xs leading-relaxed text-foreground"
        aria-label="Matching snippet"
      >
        {citation.snippet}
      </pre>
    </article>
  )
}

// ── Props ─────────────────────────────────────────────────────────────────────

export interface SearchResultsProps {
  citations: RagCitation[]
  loading: boolean
  error: string | null
  queryMs?: number | null
  onOpenFile?: (path: string) => void
}

// ── Component ─────────────────────────────────────────────────────────────────

export function SearchResults({
  citations,
  loading,
  error,
  queryMs,
  onOpenFile,
}: SearchResultsProps) {
  // Loading
  if (loading) {
    return (
      <div className="flex items-center justify-center py-10" aria-busy="true" aria-live="polite">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" aria-hidden="true" />
        <span className="sr-only">Searching…</span>
      </div>
    )
  }

  // Error
  if (error) {
    return (
      <div
        role="alert"
        className="rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive"
      >
        <span className="font-medium">Search error: </span>
        {error}
        <p className="mt-1 text-xs text-muted-foreground">
          Daemon search not yet wired (T-P4-E01-29). Showing error state.
        </p>
      </div>
    )
  }

  // No results (after a search was run)
  if (citations.length === 0) {
    return null
  }

  return (
    <section aria-label="Search results">
      {/* Result count + latency */}
      <div className="mb-3 flex items-center justify-between text-xs text-muted-foreground">
        <span>
          {citations.length} result{citations.length !== 1 ? 's' : ''}
        </span>
        {queryMs !== null && queryMs !== undefined && <span>{queryMs}ms</span>}
      </div>

      {/* Citation list */}
      <div role="list" aria-label="Citations" className="flex flex-col gap-3">
        {citations.map((c, i) => (
          <CitationCard
            key={`${c.path}-${i}`}
            citation={c}
            rank={i + 1}
            onOpenFile={onOpenFile}
          />
        ))}
      </div>
    </section>
  )
}
