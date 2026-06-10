/**
 * Purpose: React hook that fetches RAG index statistics via
 *   the Tauri command `rag_index_stats`.
 * Inputs:  None.
 * Outputs: { stats, loading, error, refetch }
 * Constraints:
 *   - Returns null stats on error; panel shows "—" placeholders.
 *   - Refetch triggered externally after ingest completes.
 * SPORT: MASTER-COMPONENTS.md — useRagIndexStats (T-P4-E01-27)
 */

import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { RagIndexStats } from './types'

interface UseRagIndexStatsResult {
  stats: RagIndexStats | null
  loading: boolean
  error: string | null
  refetch: () => void
}

export function useRagIndexStats(): UseRagIndexStatsResult {
  const [stats, setStats] = useState<RagIndexStats | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)

    invoke<RagIndexStats>('rag_index_stats')
      .then((res) => {
        if (!cancelled) {
          setStats(res)
          setLoading(false)
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setStats(null)
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [tick])

  return { stats, loading, error, refetch }
}
