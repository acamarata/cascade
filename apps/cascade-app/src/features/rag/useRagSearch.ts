/**
 * Purpose: React hook that executes a RAG search query via
 *   the Tauri command `rag_search` and returns citations.
 * Inputs:  None — exposes a `search(query)` function.
 * Outputs: { citations, loading, error, queryMs, search, clear }
 * Constraints:
 *   - Only fires when query is non-empty.
 *   - On error (daemon unavailable), returns empty citations + error string.
 *   - Does not auto-search on mount; triggered explicitly by user.
 * SPORT: MASTER-COMPONENTS.md — useRagSearch (T-P4-E01-27)
 */

import { useCallback, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { RagCitation, RagSearchResponse } from './types'

interface UseRagSearchResult {
  citations: RagCitation[]
  loading: boolean
  error: string | null
  queryMs: number | null
  search: (query: string, k?: number) => void
  clear: () => void
}

export function useRagSearch(): UseRagSearchResult {
  const [citations, setCitations] = useState<RagCitation[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [queryMs, setQueryMs] = useState<number | null>(null)

  const search = useCallback((query: string, k = 10) => {
    if (!query.trim()) return

    setLoading(true)
    setError(null)

    invoke<RagSearchResponse>('rag_search', { query: query.trim(), k })
      .then((res) => {
        setCitations(res.citations)
        setQueryMs(res.query_ms)
        setLoading(false)
      })
      .catch((err: unknown) => {
        setCitations([])
        setQueryMs(null)
        setError(err instanceof Error ? err.message : String(err))
        setLoading(false)
      })
  }, [])

  const clear = useCallback(() => {
    setCitations([])
    setQueryMs(null)
    setError(null)
  }, [])

  return { citations, loading, error, queryMs, search, clear }
}
