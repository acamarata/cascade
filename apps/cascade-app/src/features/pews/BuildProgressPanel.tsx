/**
 * BuildProgressPanel.tsx — Live build progress panel for active Build phases.
 *
 * Purpose: Show the current build unit, the observed fleet dispatch actor
 *   (from the daemon's phase status), elapsed time, and ticket pass/fail
 *   state — rendered only when a project is in "building" status. Polls
 *   phase status every 10 s (live stream TODO noted below).
 *
 * Inputs:  projectPath — absolute path to the project root.
 * Outputs: Progress panel (current ticket, timing, findings summary).
 * Constraints:
 *   - Rendered only when project phaseStatus === "building".
 *   - Calls GET /api/projects/:id/phase for live data (10 s poll interval).
 *   - TODO: replace poll with SSE / Tauri event when daemon exposes a build
 *     stream. The daemon endpoint this depends on does not yet exist.
 *   - Fleet dispatch actor is read from `last_dispatch_actor` (added by
 *     T-P7-E10-06). Null is shown honestly as "no dispatch yet" — never
 *     invented or derived from ticket weight.
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

/**
 * Purpose: Render the observed fleet dispatch actor readably for the panel.
 * Inputs:  actor — `last_dispatch_actor` from the daemon phase status, or null.
 * Outputs: A short display string. Fleet actors (`fleet/<cli>/<TaskClass>`)
 *   are parsed to `<cli> · <TaskClass>` (e.g. `claude · BulkExec`); non-fleet
 *   actors (e.g. `cascade-cli`) are shown verbatim; null is shown as
 *   "no dispatch yet". Never invents or predicts a value.
 * Constraints: Honest about null — does not derive a CLI from ticket weight.
 */
function formatDispatchActor(actor: string | null): string {
  if (!actor) return 'no dispatch yet'
  // Fleet actor format: fleet/<cli-binary>/<TaskClass>
  if (actor.startsWith('fleet/')) {
    const parts = actor.split('/')
    // parts[0] === 'fleet'; parts[1] === cli-binary; parts[2..] === task class
    if (parts.length >= 3 && parts[1] && parts[2]) {
      return `${parts[1]} · ${parts.slice(2).join('/')}`
    }
    // Malformed fleet actor — fall back to the raw observed string.
    return actor
  }
  // Non-fleet actor (e.g. "cascade-cli" from manual/CLI callers) — show as-is.
  return actor
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
 *   the endpoint is added to projects_handlers.rs. The endpoint this depends
 *   on does not yet exist; do not attempt it here.
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
  // Observed fleet dispatch actor from the daemon (T-P7-E10-06). Null when no
  // dispatch has been recorded; never invented or derived from ticket weight.
  const dispatchActor = status?.last_dispatch_actor ?? null
  const dispatchLabel = formatDispatchActor(dispatchActor)
  const hasDispatch = dispatchActor !== null

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
              {/* Observed fleet dispatch actor (T-P7-E10-05). Real value from
                  the daemon's last_dispatch_actor; null shown honestly. */}
              <span
                className="flex items-center gap-1 text-[10px] text-muted-foreground"
                title={
                  hasDispatch
                    ? `Last dispatch actor: ${dispatchActor}`
                    : 'No fleet dispatch recorded yet'
                }
              >
                <Cpu className="h-3 w-3" aria-hidden="true" />
                <span className={hasDispatch ? '' : 'italic opacity-70'}>
                  {dispatchLabel}
                </span>
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
