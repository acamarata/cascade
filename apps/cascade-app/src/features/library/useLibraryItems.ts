/**
 * Purpose: IPC hook for fetching library items from the Cascade daemon.
 *   Calls list_library_items via Tauri invoke; manages loading/error state.
 * Inputs:  Optional kind filter (undefined = all items).
 * Outputs: { items, loading, error, refetch } tuple.
 * Constraints:
 *   - No @tanstack/react-query in this project; use useState + useEffect pattern.
 *   - Re-fetches when `kind` changes.
 *   - In test/non-Tauri environments invoke is mocked; hook must not crash.
 * SPORT: MASTER-COMPONENTS.md — useLibraryItems (T-P3-E07-04)
 */

import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { LibraryItem, LibraryItemKind } from '../../types/library'

interface UseLibraryItemsResult {
  items: LibraryItem[]
  loading: boolean
  error: string | null
  refetch: () => void
}

/**
 * Fetches library items from the Cascade daemon.
 *
 * @param kind  When provided, restricts to that kind (prompt/agent/persona/slash-command).
 *              When undefined, all items are returned.
 * @returns     items array + loading/error state + refetch trigger.
 *
 * @example
 * ```tsx
 * const { items, loading, error } = useLibraryItems('prompt')
 * ```
 */
export function useLibraryItems(kind?: LibraryItemKind): UseLibraryItemsResult {
  const [items, setItems] = useState<LibraryItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)

    invoke<{ items: LibraryItem[] }>('list_library_items', kind ? { kind } : {})
      .then((result) => {
        if (!cancelled) {
          setItems(result.items)
          setLoading(false)
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [kind, tick])

  return { items, loading, error, refetch }
}
