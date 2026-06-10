/**
 * Purpose: Displays RAG index statistics: total files, total chunks,
 *   index size on disk, and last-updated timestamp.
 * Inputs:
 *   stats   — RagIndexStats or null (null when unavailable / loading).
 *   loading — shows skeleton placeholders.
 *   error   — error string or null.
 * Outputs: <section> with a stats grid.
 * Constraints:
 *   - Shows "—" for all fields when stats is null.
 *   - File size displayed in human-readable format (KB/MB).
 *   - No crash when stats.last_updated is null.
 * SPORT: MASTER-COMPONENTS.md — IndexStats (T-P4-E01-27)
 */

import type { RagIndexStats } from './types'

// ── Helpers ───────────────────────────────────────────────────────────────────

function fmtBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

function fmtDate(iso: string | null): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

// ── Stat tile ─────────────────────────────────────────────────────────────────

interface StatTileProps {
  label: string
  value: string
  loading: boolean
}

function StatTile({ label, value, loading }: StatTileProps) {
  return (
    <div className="rounded-lg border border-border bg-card p-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      {loading ? (
        <div className="mt-1 h-5 w-16 animate-pulse rounded bg-muted" aria-hidden="true" />
      ) : (
        <p className="mt-0.5 font-mono text-lg font-semibold tabular-nums text-foreground">
          {value}
        </p>
      )}
    </div>
  )
}

// ── Props ─────────────────────────────────────────────────────────────────────

interface IndexStatsProps {
  stats: RagIndexStats | null
  loading: boolean
  error: string | null
}

// ── Component ─────────────────────────────────────────────────────────────────

export function IndexStats({ stats, loading, error }: IndexStatsProps) {
  return (
    <section aria-label="Index statistics">
      <h2 className="mb-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Index Stats
      </h2>

      {error && !stats && (
        <p className="mb-2 text-xs text-muted-foreground">
          Stats unavailable — daemon not yet connected.
        </p>
      )}

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <StatTile
          label="Indexed files"
          value={stats ? stats.total_files.toLocaleString() : '—'}
          loading={loading}
        />
        <StatTile
          label="Total chunks"
          value={stats ? stats.total_chunks.toLocaleString() : '—'}
          loading={loading}
        />
        <StatTile
          label="Index size"
          value={stats ? fmtBytes(stats.index_size_bytes) : '—'}
          loading={loading}
        />
        <StatTile
          label="Last updated"
          value={stats ? fmtDate(stats.last_updated) : '—'}
          loading={loading}
        />
      </div>
    </section>
  )
}
