/**
 * Purpose: On-demand fetch of a project's PEWS scaffold tree (/api/projects/:id/scaffold).
 *   Scaffold is large JSON, so it is fetched only when "View PEWS" is clicked — never polled
 *   (CR-C: polling all projects' scaffolds every 30s would be expensive).
 * Inputs: projectId string, enabled boolean (gates the fetch until the panel opens).
 * Outputs: { data: ScaffoldTree | null, loading, error }.
 * Constraints: Aborts in-flight request on unmount, projectId change, or disable.
 * SPORT: T-P3-E02-17 useScaffold hook
 */
import { useState, useEffect } from 'react'
import { apiGet } from '@/lib/api'
import type { ScaffoldTree } from '@/lib/api'

export interface UseScaffoldResult {
  data: ScaffoldTree | null
  loading: boolean
  error: string | null
}

export function useScaffold(projectId: string, enabled: boolean): UseScaffoldResult {
  const [data, setData] = useState<ScaffoldTree | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!enabled) return
    const controller = new AbortController()
    setLoading(true)
    setError(null)
    apiGet<ScaffoldTree>(`/api/projects/${encodeURIComponent(projectId)}/scaffold`)
      .then((result) => {
        if (!controller.signal.aborted) setData(result)
      })
      .catch((err) => {
        if (!controller.signal.aborted) {
          setError(err instanceof Error ? err.message : String(err))
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [projectId, enabled])

  return { data, loading, error }
}
