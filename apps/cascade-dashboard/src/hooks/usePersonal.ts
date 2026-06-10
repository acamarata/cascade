/**
 * Purpose: Generic data-fetching hook for Personal daemon endpoints with 30s auto-refresh.
 * Inputs: endpoint URL string (e.g. '/api/personal/threads').
 * Outputs: { data: T | null, loading: boolean, error: string | null, refetch: () => void }
 * Constraints: Clears interval on unmount to prevent memory leaks. Error stored as string.
 *   Mirrors useGCI — same pattern, separate hook for Personal endpoints.
 * SPORT: T-P3-E02-12 usePersonal hook
 */
import { useState, useEffect, useCallback, useRef } from 'react'
import { apiGet } from '@/lib/api'

const REFRESH_INTERVAL_MS = 30_000

export interface UsePersonalResult<T> {
  data: T | null
  loading: boolean
  error: string | null
  refetch: () => void
}

export function usePersonal<T>(endpoint: string): UsePersonalResult<T> {
  const [data, setData] = useState<T | null>(null)
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
      const result = await apiGet<T>(endpoint)
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
