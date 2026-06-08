/**
 * Purpose: Data-fetching hook for the Projects daemon endpoint with 30s auto-refresh.
 * Inputs: endpoint URL string (default '/api/projects').
 * Outputs: { data: ProjectsResponse | null, loading, error, refetch } — mirrors usePersonal.
 * Constraints: Aborts in-flight request on unmount/refetch to prevent state-after-unmount.
 *   Separate hook from usePersonal so the Projects route polls independently.
 * SPORT: T-P3-E02-16 useProjects hook
 */
import { useState, useEffect, useCallback, useRef } from 'react'
import { apiGet } from '@/lib/api'
import type { ProjectsResponse } from '@/lib/api'

const REFRESH_INTERVAL_MS = 30_000

export interface UseProjectsResult {
  data: ProjectsResponse | null
  loading: boolean
  error: string | null
  refetch: () => void
}

export function useProjects(endpoint = '/api/projects'): UseProjectsResult {
  const [data, setData] = useState<ProjectsResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const fetch_ = useCallback(async () => {
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setError(null)
    try {
      const result = await apiGet<ProjectsResponse>(endpoint)
      if (!controller.signal.aborted) {
        setData(result)
      }
    } catch (err) {
      if (!controller.signal.aborted) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      if (!controller.signal.aborted) {
        setLoading(false)
      }
    }
  }, [endpoint])

  useEffect(() => {
    void fetch_()
    const id = setInterval(() => { void fetch_() }, REFRESH_INTERVAL_MS)
    return () => {
      clearInterval(id)
      abortRef.current?.abort()
    }
  }, [fetch_])

  return { data, loading, error, refetch: fetch_ }
}
