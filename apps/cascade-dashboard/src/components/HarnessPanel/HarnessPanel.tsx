/**
 * Purpose: Harness Augmentation panel — shows CC and OC integration status and
 *   provides per-harness instruction-file regeneration.
 * Inputs: none (self-fetching on mount via /api/gci/harness-status).
 * Outputs: Two HarnessCards (cc, oc) with detected/linked badges and Regenerate buttons.
 *   Structured so a RAG status card (T-P3-E02-29) can be appended as a sibling section.
 * Constraints: fetch() only (no apiGet helper for POST). No any. <300 lines.
 *   Tailwind + lucide-react for styling; no shadcn (not installed in this app).
 * SPORT: T-P3-E02-28
 */
import { useEffect, useState } from 'react'
import { RefreshCw, CheckCircle2, XCircle, FileCode2 } from 'lucide-react'
import { type HarnessEntry, type HarnessStatus, type RegenerateResponse } from '@/lib/api'
import { RagStatusCard } from './RagStatusCard'

// ── types ──────────────────────────────────────────────────────────────────

interface CardState {
  loading: boolean
  toast: string | null
  error: string | null
}

// ── sub-component ──────────────────────────────────────────────────────────

interface HarnessCardProps {
  entry: HarnessEntry
  onRegenerate: (harness: string) => Promise<void>
  cardState: CardState
}

function HarnessCard({ entry, onRegenerate, cardState }: HarnessCardProps) {
  const harnessLabel = entry.name === 'cc' ? 'Claude Code (CC)' : 'OpenCode (OC)'

  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-3">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <FileCode2 className="w-4 h-4 text-muted-foreground" />
          <span className="text-sm font-semibold text-foreground">{harnessLabel}</span>
        </div>
        {/* Detected badge */}
        {entry.detected ? (
          <span className="inline-flex items-center gap-1 text-xs font-medium px-2 py-0.5 rounded-full bg-green-500/15 text-green-600 dark:text-green-400">
            <CheckCircle2 className="w-3 h-3" />
            Detected
          </span>
        ) : (
          <span className="inline-flex items-center gap-1 text-xs font-medium px-2 py-0.5 rounded-full bg-muted text-muted-foreground">
            <XCircle className="w-3 h-3" />
            Not detected
          </span>
        )}
      </div>

      {/* Instruction file row */}
      <div className="flex flex-col gap-1">
        <span className="text-xs text-muted-foreground font-medium">Instruction file</span>
        <div className="flex items-center gap-2">
          {entry.instructionFileLinked ? (
            <CheckCircle2 className="w-3.5 h-3.5 shrink-0 text-green-500" />
          ) : (
            <XCircle className="w-3.5 h-3.5 shrink-0 text-muted-foreground" />
          )}
          <span className="text-xs text-foreground font-mono break-all">
            {entry.instructionFilePath || 'Not linked'}
          </span>
        </div>
      </div>

      {/* Regenerate button + feedback */}
      <div className="flex flex-col gap-1.5 mt-auto pt-1">
        <button
          onClick={() => onRegenerate(entry.name)}
          disabled={cardState.loading}
          className="inline-flex items-center justify-center gap-1.5 text-xs font-medium px-3 py-1.5 rounded border border-border bg-background hover:bg-muted transition-colors disabled:opacity-60 disabled:cursor-not-allowed"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${cardState.loading ? 'animate-spin' : ''}`} />
          {cardState.loading ? 'Regenerating…' : 'Regenerate instruction files'}
        </button>
        {cardState.toast && (
          <p className="text-xs text-green-600 dark:text-green-400">{cardState.toast}</p>
        )}
        {cardState.error && (
          <p className="text-xs text-red-500">{cardState.error}</p>
        )}
      </div>
    </div>
  )
}

// ── main panel ─────────────────────────────────────────────────────────────

export function HarnessPanel() {
  const [data, setData] = useState<HarnessStatus | null>(null)
  const [fetchError, setFetchError] = useState<string | null>(null)
  const [cardStates, setCardStates] = useState<Record<string, CardState>>({})

  // Fetch harness status on mount
  useEffect(() => {
    fetch('/api/gci/harness-status')
      .then((res) => {
        if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
        return res.json() as Promise<HarnessStatus>
      })
      .then((d) => setData(d))
      .catch((err: Error) => setFetchError(err.message))
  }, [])

  const handleRegenerate = async (harness: string) => {
    setCardStates((prev) => ({
      ...prev,
      [harness]: { loading: true, toast: null, error: null },
    }))
    try {
      const res = await fetch('/api/gci/harness-regenerate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ harness }),
      })
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `${res.status} ${res.statusText}`)
      }
      const json = (await res.json()) as RegenerateResponse
      setCardStates((prev) => ({
        ...prev,
        [harness]: {
          loading: false,
          toast: `Regenerated ${json.regeneratedCount} link${json.regeneratedCount !== 1 ? 's' : ''}`,
          error: null,
        },
      }))
      // Refresh status after successful regeneration
      fetch('/api/gci/harness-status')
        .then((r) => r.json() as Promise<HarnessStatus>)
        .then((d) => setData(d))
        .catch(() => undefined)
    } catch (err) {
      setCardStates((prev) => ({
        ...prev,
        [harness]: {
          loading: false,
          toast: null,
          error: err instanceof Error ? err.message : 'Regeneration failed',
        },
      }))
    }
  }

  return (
    <section id="harness-panel" className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold text-foreground">Harness Augmentation</h2>
        <p className="text-xs text-muted-foreground mt-0.5">
          Cascade integration status for supported AI harnesses (CC, OC).
        </p>
      </div>

      {/* Loading skeleton */}
      {!data && !fetchError && (
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          {['cc', 'oc'].map((h) => (
            <div key={h} className="rounded-lg border border-border bg-card p-4 animate-pulse h-36" />
          ))}
        </div>
      )}

      {/* Error */}
      {fetchError && (
        <p className="text-sm text-red-500">Failed to load harness status: {fetchError}</p>
      )}

      {/* Cards */}
      {data && (
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          {data.harnesses.map((entry) => (
            <HarnessCard
              key={entry.name}
              entry={entry}
              onRegenerate={handleRegenerate}
              cardState={cardStates[entry.name] ?? { loading: false, toast: null, error: null }}
            />
          ))}
        </div>
      )}

      {/* RAG/RRF serve status card (T-P3-E02-29) */}
      <RagStatusCard />
    </section>
  )
}
