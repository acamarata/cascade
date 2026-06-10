/**
 * Purpose: React hook that fetches the list of indexed RAG sources
 *   by invoking the Tauri command `rag_list_sources`.
 *   Returns loading / error / data states; provides a `refetch` trigger.
 * Inputs:  None (self-contained).
 * Outputs: { sources, loading, error, refetch }
 * Constraints:
 *   - On error, surfaces a string message; does NOT throw.
 *   - If the daemon is unavailable, returns empty sources + error string
 *     so the panel renders an empty-state rather than crashing.
 *   - Does not auto-poll; caller must call refetch() after a drop/ingest.
 * SPORT: MASTER-COMPONENTS.md — useRagSources (T-P4-E01-27)
 */

import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { RagListSourcesResponse, RagSource } from './types'

interface UseRagSourcesResult {
  sources: RagSource[]
  loading: boolean
  error: string | null
  refetch: () => void
}

export function useRagSources(): UseRagSourcesResult {
  const [sources, setSources] = useState<RagSource[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)

    invoke<RagListSourcesResponse>('rag_list_sources')
      .then((res) => {
        if (!cancelled) {
          setSources(res.sources)
          setLoading(false)
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          // Daemon not yet wired (T-P4-E01-29): show graceful empty state.
          setSources([])
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [tick])

  return { sources, loading, error, refetch }
}
