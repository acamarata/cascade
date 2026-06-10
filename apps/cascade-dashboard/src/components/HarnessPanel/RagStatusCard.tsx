/**
 * Purpose: RAG/RRF serve-status card displayed in the HarnessPanel, showing whether
 *   the cascade context-serving pipeline is active and the current index state.
 * Inputs: none — self-fetching from GET /api/gci/rag-status on mount.
 * Outputs: Card with serving status pill, endpoint URL, index count, size, and last
 *   indexed timestamp. P3 renders "No index" (stub values from daemon).
 * Constraints: fetch() only. No any. Tailwind + lucide-react + date-fns. <300 lines.
 *   Handle loading and error states gracefully. last_indexed null → shows "—".
 * SPORT: T-P3-E02-29
 */
import { useEffect, useState } from 'react'
import { Database, Circle, AlertCircle } from 'lucide-react'
import { formatDistanceToNow } from 'date-fns'
import { type RagStatusResponse } from '@/lib/api'

// ── helpers ────────────────────────────────────────────────────────────────

/**
 * Format bytes as a human-readable string.
 * @param bytes - Raw byte count.
 * @returns Formatted string e.g. "0 B", "4.2 KB", "1.3 MB".
 */
function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const exp = Math.min(Math.floor(Math.log2(bytes) / 10), units.length - 1)
  const value = bytes / Math.pow(1024, exp)
  return `${value % 1 === 0 ? value : value.toFixed(1)} ${units[exp]}`
}

/**
 * Derive status pill label + style from RagStatusResponse fields.
 * serving=true  → green  "Serving"
 * serving=false + index_count=0 → yellow "No index"
 * serving=false + index_count>0 → grey   "Stopped"
 */
type PillVariant = 'serving' | 'no-index' | 'stopped'

function resolvePill(data: RagStatusResponse): PillVariant {
  if (data.serving) return 'serving'
  if (data.index_count === 0) return 'no-index'
  return 'stopped'
}

const PILL_STYLES: Record<PillVariant, { dot: string; label: string; text: string }> = {
  serving: {
    dot: 'bg-green-500',
    label: 'Serving',
    text: 'bg-green-500/15 text-green-600 dark:text-green-400',
  },
  'no-index': {
    dot: 'bg-yellow-500',
    label: 'No index',
    text: 'bg-yellow-500/15 text-yellow-600 dark:text-yellow-400',
  },
  stopped: {
    dot: 'bg-muted-foreground',
    label: 'Stopped',
    text: 'bg-muted text-muted-foreground',
  },
}

// ── component ──────────────────────────────────────────────────────────────

/** Displays the RAG/RRF serve status from the cascade daemon. */
export function RagStatusCard() {
  const [data, setData] = useState<RagStatusResponse | null>(null)
  const [fetchError, setFetchError] = useState<string | null>(null)

  useEffect(() => {
    fetch('/api/gci/rag-status')
      .then((res) => {
        if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
        return res.json() as Promise<RagStatusResponse>
      })
      .then((d) => setData(d))
      .catch((err: Error) => setFetchError(err.message))
  }, [])

  // Loading skeleton
  if (!data && !fetchError) {
    return (
      <div className="rounded-lg border border-border bg-card p-4 animate-pulse h-28 mt-3" />
    )
  }

  // Error state
  if (fetchError) {
    return (
      <div className="rounded-lg border border-border bg-card p-4 flex items-center gap-2 mt-3">
        <AlertCircle className="w-4 h-4 text-red-500 shrink-0" />
        <span className="text-xs text-red-500">RAG status unavailable: {fetchError}</span>
      </div>
    )
  }

  // Loaded state (data is non-null here)
  const pill = resolvePill(data!)
  const pillStyle = PILL_STYLES[pill]

  const lastIndexedText: string = data!.last_indexed
    ? formatDistanceToNow(new Date(data!.last_indexed), { addSuffix: true })
    : '—'

  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-3 mt-3">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Database className="w-4 h-4 text-muted-foreground" />
          <span className="text-sm font-semibold text-foreground">RAG / RRF Context Serve</span>
        </div>

        {/* Status pill */}
        <span
          className={`inline-flex items-center gap-1.5 text-xs font-medium px-2 py-0.5 rounded-full ${pillStyle.text}`}
        >
          <Circle className={`w-2 h-2 fill-current ${pillStyle.dot} rounded-full`} />
          {pillStyle.label}
        </span>
      </div>

      {/* Stats grid */}
      <dl className="grid grid-cols-2 gap-x-4 gap-y-2">
        {/* Endpoint */}
        <div className="col-span-2 flex flex-col gap-0.5">
          <dt className="text-xs text-muted-foreground font-medium">Endpoint</dt>
          <dd className="text-xs font-mono text-foreground break-all">{data!.serve_endpoint}</dd>
        </div>

        {/* Index count */}
        <div className="flex flex-col gap-0.5">
          <dt className="text-xs text-muted-foreground font-medium">Entries</dt>
          <dd className="text-xs text-foreground tabular-nums">
            {data!.index_count.toLocaleString()}
          </dd>
        </div>

        {/* Index size */}
        <div className="flex flex-col gap-0.5">
          <dt className="text-xs text-muted-foreground font-medium">Index size</dt>
          <dd className="text-xs text-foreground tabular-nums">{formatBytes(data!.index_size_bytes)}</dd>
        </div>

        {/* Last indexed */}
        <div className="col-span-2 flex flex-col gap-0.5">
          <dt className="text-xs text-muted-foreground font-medium">Last indexed</dt>
          <dd className="text-xs text-foreground">{lastIndexedText}</dd>
        </div>
      </dl>
    </div>
  )
}
