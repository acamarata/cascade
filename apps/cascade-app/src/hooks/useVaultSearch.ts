/**
 * Purpose: Debounced vault full-text search hook — wraps the `vault_search` Tauri IPC
 *   command and exposes query state, debounced invocation, and result/error state.
 * Inputs:
 *   - root: absolute vault root path (may be empty/undefined — skips search when absent)
 *   - debounceMs: debounce delay in milliseconds (default 300)
 * Outputs: { query, setQuery, results, isLoading, error }
 * Constraints:
 *   - No search is issued when query.trim() is empty.
 *   - Debounce timer is cleared on unmount (no stale callbacks).
 *   - IPC call uses invoke<SearchMatch[]>("vault_search") — Tauri serialisation.
 *   - Error is typed as string for simplicity; callers render it directly.
 * SPORT: MASTER-HOOKS.md — useVaultSearch (T-P3-E06-09)
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { SearchMatch } from '@/types/vault'

interface UseVaultSearchOptions {
  /** Absolute vault root. Search is skipped when absent or empty. */
  root: string | undefined
  /** Debounce delay in ms (default: 300). */
  debounceMs?: number
}

interface UseVaultSearchResult {
  /** Current query string (controlled). */
  query: string
  /** Update the query; triggers a debounced search. */
  setQuery: (q: string) => void
  /** Search results from the last successful invocation. */
  results: SearchMatch[]
  /** True while a search is in flight. */
  isLoading: boolean
  /** Error message from the last failed invocation, or null. */
  error: string | null
}

export function useVaultSearch({
  root,
  debounceMs = 300,
}: UseVaultSearchOptions): UseVaultSearchResult {
  const [query, setQueryRaw] = useState('')
  const [results, setResults] = useState<SearchMatch[]>([])
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Timer ref — cleared on unmount.
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // AbortController ref so stale requests are ignored.
  const seqRef = useRef(0)

  const runSearch = useCallback(
    async (q: string) => {
      if (!root || q.trim() === '') {
        setResults([])
        setIsLoading(false)
        return
      }

      const seq = ++seqRef.current
      setIsLoading(true)
      setError(null)

      try {
        const hits = await invoke<SearchMatch[]>('vault_search', { root, query: q })
        // Ignore stale responses.
        if (seq !== seqRef.current) return
        setResults(hits)
      } catch (err) {
        if (seq !== seqRef.current) return
        const msg =
          err && typeof err === 'object' && 'message' in err
            ? String((err as { message: unknown }).message)
            : String(err)
        setError(msg)
        setResults([])
      } finally {
        if (seq === seqRef.current) setIsLoading(false)
      }
    },
    [root],
  )

  const setQuery = useCallback(
    (q: string) => {
      setQueryRaw(q)
      if (timerRef.current) clearTimeout(timerRef.current)

      if (q.trim() === '') {
        setResults([])
        setIsLoading(false)
        setError(null)
        return
      }

      timerRef.current = setTimeout(() => {
        void runSearch(q)
      }, debounceMs)
    },
    [debounceMs, runSearch],
  )

  // Clear timer on unmount.
  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  return { query, setQuery, results, isLoading, error }
}
