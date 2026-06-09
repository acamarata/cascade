/**
 * Purpose: Fetch hook for GET /api/personal/usage-history with configurable day range.
 * Inputs: days — number of days to fetch (1–90, enforced server-side).
 * Outputs: { data, loading, error, refetch } where data is UsageHistoryResponse | null.
 * Constraints: Re-fetches on days change; aborts in-flight request on cleanup.
 *   Does NOT use the 30s auto-refresh from usePersonal — caller controls refetch.
 *   Max 90 days enforced by the server (returns 400 for >90).
 * SPORT: T-P3-E02-24 useUsageHistory hook
 */
import { useState, useEffect, useCallback, useRef } from 'react'
import { apiGet, type UsageHistoryResponse } from '@/lib/api'

export interface UseUsageHistoryResult {
  data: UsageHistoryResponse | null
  loading: boolean
  error: string | null
  refetch: () => void
}

export function useUsageHistory(days: number): UseUsageHistoryResult {
  const [data, setData] = useState<UsageHistoryResponse | null>(null)
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
      const result = await apiGet<UsageHistoryResponse>(
        `/api/personal/usage-history?days=${days}`,
      )
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
  }, [days])

  useEffect(() => {
    void fetch_()
    return () => {
      abortRef.current?.abort()
    }
  }, [fetch_])

  return { data, loading, error, refetch: fetch_ }
}
