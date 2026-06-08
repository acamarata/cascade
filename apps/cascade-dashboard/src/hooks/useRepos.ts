/**
 * Purpose: On-demand fetch of a project's git repos (/api/projects/:id/repos). Unlike
 *   useProjects this does NOT poll — it runs once when a ProjectCard expands.
 * Inputs: projectId string.
 * Outputs: { data: ReposResponse | null, loading, error }.
 * Constraints: Aborts in-flight request on unmount or projectId change to avoid setting state
 *   after the card collapses (CR-A leak guard). Refetches when projectId changes.
 * SPORT: T-P3-E02-17 useRepos hook
 */
import { useState, useEffect } from 'react'
import { apiGet } from '@/lib/api'
import type { ReposResponse } from '@/lib/api'

export interface UseReposResult {
  data: ReposResponse | null
  loading: boolean
  error: string | null
}

export function useRepos(projectId: string): UseReposResult {
  const [data, setData] = useState<ReposResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError(null)
    apiGet<ReposResponse>(`/api/projects/${encodeURIComponent(projectId)}/repos`)
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
  }, [projectId])

  return { data, loading, error }
}
