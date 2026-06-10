/**
 * Purpose: React hook that wraps the upsert_library_item IPC command.
 *   Provides a typed async mutate function, loading/error state, and a
 *   success callback for cache invalidation (simple event-based since
 *   react-query is not in this project's deps).
 * Inputs:
 *   - options.onSuccess: (ack) => void — called after a successful upsert
 *   - options.onError: (err) => void — called on IPC error
 * Outputs: { mutate, isPending, error } — mirrors the react-query useMutation API shape.
 * Constraints:
 *   - All IPC calls gracefully degrade: command-not-found → error surfaced via onError.
 *   - Never throws — errors are reported via onError.
 *   - isPending is true only during an active IPC call (not after success/error).
 * SPORT: MASTER-COMPONENTS.md — useUpsertLibraryItem — features/library/ — T-P3-E07-06
 */

import { useState, useCallback } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { LibraryItem, UpsertLibraryItemAck } from './types'

// ── Options ───────────────────────────────────────────────────────────────────

export interface UseUpsertLibraryItemOptions {
  onSuccess?: (ack: UpsertLibraryItemAck) => void
  onError?: (err: string) => void
}

// ── Return shape ──────────────────────────────────────────────────────────────

export interface UseUpsertLibraryItemResult {
  mutate: (item: LibraryItem) => Promise<void>
  isPending: boolean
  error: string | null
  reset: () => void
}

// ── Hook ──────────────────────────────────────────────────────────────────────

/**
 * Wraps the upsert_library_item Tauri IPC command.
 *
 * On success: calls onSuccess(ack) and emits a "library-items-changed" window
 * event so LibraryPanel (and any other listener) can refresh its item list.
 *
 * On error: calls onError(message) and sets the error state; never throws.
 */
export function useUpsertLibraryItem(
  options: UseUpsertLibraryItemOptions = {}
): UseUpsertLibraryItemResult {
  const [isPending, setIsPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const mutate = useCallback(
    async (item: LibraryItem): Promise<void> => {
      setIsPending(true)
      setError(null)
      try {
        const ack = await invoke<UpsertLibraryItemAck>('upsert_library_item', { item })
        // Notify panel listeners that the item list has changed.
        window.dispatchEvent(new CustomEvent('library-items-changed', { detail: { id: ack.id } }))
        options.onSuccess?.(ack)
      } catch (err) {
        const msg =
          err instanceof Error
            ? err.message
            : typeof err === 'string'
              ? err
              : 'upsert_library_item failed'
        setError(msg)
        options.onError?.(msg)
      } finally {
        setIsPending(false)
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [options.onSuccess, options.onError]
  )

  const reset = useCallback(() => {
    setError(null)
    setIsPending(false)
  }, [])

  return { mutate, isPending, error, reset }
}
