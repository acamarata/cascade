/**
 * useProjectRegistry.ts — Fetch the project list from the cascade daemon.
 *
 * Purpose: Calls GET http://127.0.0.1:9761/api/projects and returns a
 *   deduplicated list of RegistryProject entries, excluding any entry whose id
 *   is "personal" or whose path contains a "personal" segment (per pews-03 spec).
 *   Phase status is fetched in parallel for each project.
 *
 * Inputs:  None (reads from daemon HTTP API, port 9761).
 * Outputs: { projects, loading, error, refetch }
 * Constraints:
 *   - Does NOT call the Tauri IPC layer — uses fetch() against the daemon HTTP server.
 *   - Personal workspaces (id === "personal" or path segment === "personal") excluded.
 *   - Falls back to empty array on network error so the UI degrades gracefully.
 *   - Refreshes on mount + window focus.
 * SPORT: MASTER-COMPONENTS.md — useProjectRegistry (pews-03)
 */

import { useCallback, useEffect, useRef, useState } from 'react'

// ── Daemon HTTP base URL ──────────────────────────────────────────────────────

const DAEMON_BASE = 'http://127.0.0.1:9761'

// ── Types mirroring projects_handlers.rs ─────────────────────────────────────

/** One project entry from GET /api/projects (mirrors ProjectEntry in Rust). */
export interface DaemonProjectEntry {
  id: string
  name: string
  path: string
  description: string
  repo_count: number
  active_phase_id: string | null
}

/** Phase status from GET /api/projects/:id/phase (mirrors PhaseStatus in Rust). */
export interface DaemonPhaseStatus {
  phase_id: string | null
  phase_name: string | null
  phase_status: string | null
  pct_done: number | null
  tickets_total: number | null
  tickets_done: number | null
  tickets_blocked: number | null
  plan_md_exists: boolean
  phase_dir: string | null
}

/** Combined entry exposed to UI components. */
export interface RegistryProject {
  id: string
  name: string
  path: string
  description: string
  repoCount: number
  activePhaseId: string | null
  /** Phase lifecycle status string, e.g. "building", "planning", "shipped". */
  phaseStatus: string | null
  /** Percentage complete 0–100. */
  pctDone: number
  /** Count of blocked/open findings (tickets_blocked from phase status). */
  openFindings: number
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function isPersonal(entry: DaemonProjectEntry): boolean {
  if (entry.id.toLowerCase() === 'personal') return true
  const segments = entry.path.split(/[\\/]/)
  return segments.some((s) => s.toLowerCase() === 'personal')
}

async function fetchProjects(): Promise<RegistryProject[]> {
  const res = await fetch(`${DAEMON_BASE}/api/projects`)
  if (!res.ok) throw new Error(`GET /api/projects → ${res.status}`)
  const data = (await res.json()) as { projects: DaemonProjectEntry[] }
  const entries = (data.projects ?? []).filter((e) => !isPersonal(e))

  // Fetch phase status for each project in parallel.
  const withPhase = await Promise.all(
    entries.map(async (entry): Promise<RegistryProject> => {
      let phaseStatus: string | null = null
      let pctDone = 0
      let openFindings = 0
      try {
        const pr = await fetch(`${DAEMON_BASE}/api/projects/${encodeURIComponent(entry.id)}/phase`)
        if (pr.ok) {
          const ps = (await pr.json()) as DaemonPhaseStatus
          phaseStatus = ps.phase_status ?? null
          pctDone = ps.pct_done ?? 0
          openFindings = ps.tickets_blocked ?? 0
        }
      } catch {
        // Phase status is best-effort — continue without it.
      }
      return {
        id: entry.id,
        name: entry.name,
        path: entry.path,
        description: entry.description,
        repoCount: entry.repo_count,
        activePhaseId: entry.active_phase_id,
        phaseStatus,
        pctDone,
        openFindings,
      }
    })
  )

  return withPhase
}

// ── Hook ─────────────────────────────────────────────────────────────────────

export interface UseProjectRegistryResult {
  projects: RegistryProject[]
  loading: boolean
  error: string | null
  refetch: () => void
}

export function useProjectRegistry(): UseProjectRegistryResult {
  const [projects, setProjects] = useState<RegistryProject[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refetch = useCallback(() => {
    abortRef.current?.abort()
    abortRef.current = new AbortController()
    setLoading(true)
    setError(null)
    fetchProjects()
      .then((ps) => {
        setProjects(ps)
        setLoading(false)
      })
      .catch((err: unknown) => {
        if (err instanceof Error && err.name === 'AbortError') return
        setError(err instanceof Error ? err.message : String(err))
        setLoading(false)
      })
  }, [])

  useEffect(() => {
    refetch()
    const onFocus = () => refetch()
    window.addEventListener('focus', onFocus)
    return () => {
      window.removeEventListener('focus', onFocus)
      abortRef.current?.abort()
    }
  }, [refetch])

  return { projects, loading, error, refetch }
}
