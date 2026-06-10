/**
 * Purpose: Displays an ingest progress bar while a file is being chunked
 *   and indexed. Shows file name, chunks-done / chunks-total, and a
 *   percentage progress bar. Disappears when not active.
 * Inputs:
 *   progress — RagIngestProgress payload or null when idle.
 *   active   — true while ingest is in flight.
 * Outputs: <div> progress bar, or null when inactive.
 * Constraints:
 *   - Uses role="progressbar" with aria-valuenow/min/max for a11y.
 *   - Renders null when active=false and progress=null (no flicker).
 * SPORT: MASTER-COMPONENTS.md — IngestProgress (T-P4-E01-27)
 */

import { Loader2 } from 'lucide-react'
import type { RagIngestProgress } from './types'

interface IngestProgressProps {
  progress: RagIngestProgress | null
  active: boolean
}

export function IngestProgress({ progress, active }: IngestProgressProps) {
  if (!active && !progress) return null

  const pct = progress?.percent ?? 0
  const fileName = progress?.file_name ?? ''
  const chunksDone = progress?.chunks_done ?? 0
  const chunksTotal = progress?.chunks_total ?? 0
  const done = progress?.done ?? false

  return (
    <div
      className="rounded-lg border border-border bg-muted/30 px-4 py-3"
      aria-label="Ingest progress"
      data-testid="ingest-progress"
    >
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          {!done && (
            <Loader2
              className="h-3.5 w-3.5 shrink-0 animate-spin text-primary"
              aria-hidden="true"
            />
          )}
          <span className="truncate text-sm font-medium text-foreground" title={fileName}>
            {fileName || 'Ingesting…'}
          </span>
        </div>
        <span className="shrink-0 tabular-nums text-xs text-muted-foreground">
          {chunksTotal > 0 ? `${chunksDone} / ${chunksTotal} chunks` : `${pct}%`}
        </span>
      </div>

      {/* Progress bar */}
      <div
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={`Ingest progress: ${pct}%`}
        className="h-1.5 w-full overflow-hidden rounded-full bg-muted"
      >
        <div
          className={[
            'h-full rounded-full transition-all duration-200',
            done ? 'bg-green-500' : 'bg-primary',
          ].join(' ')}
          style={{ width: `${pct}%` }}
        />
      </div>

      {done && (
        <p className="mt-1.5 text-xs text-green-600 dark:text-green-400">Index complete.</p>
      )}
    </div>
  )
}
