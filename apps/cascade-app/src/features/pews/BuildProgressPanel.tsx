/**
 * BuildProgressPanel.tsx — Live build progress panel for active Build phases.
 *
 * Purpose: Show the current build unit, fleet account/model (from project state),
 *   elapsed time, and ticket pass/fail state — rendered only when a project is in
 *   "building" status. Polls phase status every 10 s (live stream TODO noted below).
 *
 * Inputs:  projectPath — absolute path to the project root.
 * Outputs: Progress panel (current ticket, timing, findings summary).
 * Constraints:
 *   - Rendered only when project phaseStatus === "building".
 *   - Calls GET /api/projects/:id/phase for live data (10 s poll interval).
 *   - TODO: replace poll with SSE / Tauri event when daemon exposes a build stream.
 *   - Fleet account/model info not yet in the API — shown as a stub with TODO.
 *   - Elapsed time measured from component mount (approximation until API provides start_at).
 * SPORT: MASTER-COMPONENTS.md — BuildProgressPanel (pews-03)
 */

import { useEffect, useRef, useState } from 'react'
import { Loader2, CheckCircle2, XCircle, Clock, Cpu } from 'lucide-react'
import type { DaemonPhaseStatus } from './useProjectRegistry'

// ── Daemon base URL ───────────────────────────────────────────────────────────

const DAEMON_BASE = 'http://127.0.0.1:9761'

// ── Helpers ───────────────────────────────────────────────────────────────────

function formatElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  return `${m}m ${s}s`
}

// ── Component ─────────────────────────────────────────────────────────────────

export interface BuildProgressPanelProps {
  /** Absolute path to the project whose build is active. */
  projectPath: string
  /** Short project id (directory name) for the daemon API call. */
  projectId: string
}

/**
 * Purpose: Panel rendered when a project's phase_status is "building". Shows
 *   current phase progress and ticket stats. Polls every 10 s.
 *
 * TODO(pews-04): Replace 10 s poll with daemon SSE build-progress stream when
 *   the endpoint is added to projects_handlers.rs.
 * TODO(pews-04): Add fleet account/model column once the daemon exposes it in
 *   the phase-status or a new /api/projects/:id/build endpoint.
 */
export function BuildProgressPanel({
  projectPath: _projectPath,
  projectId,
}: BuildProgressPanelProps) {
  const [status, setStatus] = useState<DaemonPhaseStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [elapsed, setElapsed] = useState(0)
  // Store mount time in an effect so Date.now() is not called during render.
  const mountTimeRef = useRef<number>(0)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  // Elapsed timer — runs every second while component is mounted.
  useEffect(() => {
    mountTimeRef.current = Date.now()
    timerRef.current = setInterval(() => {
      setElapsed(Math.floor((Date.now() - mountTimeRef.current) / 1000))
    }, 1000)
    return () => {
      if (timerRef.current) clearInterval(timerRef.current)
    }
  }, [])

  // Poll phase status every 10 s.
  useEffect(() => {
    let cancelled = false

    async function poll() {
      try {
        const res = await fetch(
          `${DAEMON_BASE}/api/projects/${encodeURIComponent(projectId)}/phase`
        )
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const ps = (await res.json()) as DaemonPhaseStatus
        if (!cancelled) {
          setStatus(ps)
          setLoading(false)
          setError(null)
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      }
    }

    void poll()
    const interval = setInterval(() => void poll(), 10_000)
    return () => {
      cancelled = true
      clearInterval(interval)
    }
  }, [projectId])

  const pct = status?.pct_done ?? 0
  const done = status?.tickets_done ?? 0
  const total = status?.tickets_total ?? 0
  const blocked = status?.tickets_blocked ?? 0
  const phaseName = status?.phase_name ?? status?.phase_id ?? 'Active Phase'

  return (
    <div
      className="flex shrink-0 items-start gap-4 border-b border-amber-400/20 bg-amber-400/5 px-4 py-3"
      role="region"
      aria-label="Build progress"
      data-testid="build-progress-panel"
    >
      {/* Build indicator */}
      <div className="flex shrink-0 flex-col items-center gap-1">
        <div className="flex h-8 w-8 items-center justify-center rounded-full bg-amber-400/15">
          <Loader2 className="h-4 w-4 animate-spin text-amber-400" aria-hidden="true" />
        </div>
        <span className="text-[9px] font-semibold uppercase tracking-wider text-amber-400">
          Building
        </span>
      </div>

      {/* Phase info */}
      <div className="min-w-0 flex-1">
        {loading ? (
          <div className="space-y-1.5">
            <div className="h-3 w-32 animate-pulse rounded bg-muted/30" />
            <div className="h-2 w-48 animate-pulse rounded bg-muted/20" />
          </div>
        ) : error ? (
          <p className="text-xs text-destructive">Build status unavailable: {error}</p>
        ) : (
          <div className="space-y-1.5">
            <div className="flex flex-wrap items-center gap-3">
              <span className="text-xs font-semibold text-foreground">{phaseName}</span>
              {/* Elapsed */}
              <span className="flex items-center gap-1 text-[10px] text-muted-foreground">
                <Clock className="h-3 w-3" aria-hidden="true" />
                {formatElapsed(elapsed)}
              </span>
              {/* Fleet stub — TODO(pews-04): replace with real account/model from daemon */}
              <span
                className="flex items-center gap-1 text-[10px] text-muted-foreground/60"
                title="Fleet account/model — available in a future daemon update (pews-04)"
              >
                <Cpu className="h-3 w-3" aria-hidden="true" />
                <span className="opacity-50">fleet: —</span>
              </span>
            </div>

            {/* Progress bar */}
            <div className="flex items-center gap-2">
              <div
                className="h-1.5 flex-1 max-w-xs rounded-full bg-border"
                role="progressbar"
                aria-valuenow={pct}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-label={`${pct.toFixed(0)}% complete`}
              >
                <div
                  className="h-full rounded-full bg-amber-400 transition-all duration-500"
                  style={{ width: `${Math.max(0, Math.min(100, pct))}%` }}
                />
              </div>
              <span className="text-[10px] text-muted-foreground tabular-nums">
                {pct.toFixed(0)}%
              </span>
            </div>

            {/* Ticket stats */}
            <div className="flex items-center gap-3">
              {total > 0 && (
                <span className="flex items-center gap-1 text-[10px] text-muted-foreground">
                  <CheckCircle2 className="h-3 w-3 text-green-400" aria-hidden="true" />
                  {done}/{total} tickets
                </span>
              )}
              {blocked > 0 && (
                <span className="flex items-center gap-1 text-[10px] text-destructive">
                  <XCircle className="h-3 w-3" aria-hidden="true" />
                  {blocked} blocked
                </span>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
