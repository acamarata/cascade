/**
 * usePewsTree.ts — React hook for PEWS tree state with live sync and mutation.
 *
 * Purpose: Loads the full PEWS tree from the Tauri backend, installs a file-watch
 *   listener for live disk→app sync, and exposes a mutation function for optimistic
 *   ticket status updates (app→disk).
 *
 * Inputs:  phaseRoot — absolute path to the project root.
 * Outputs: { tree, loading, error, updateTicketStatus, refetch }
 *
 * Live sync strategy:
 *   - On mount: calls pbd_watch to install a notify watcher in the Tauri backend.
 *   - Tauri emits "pbd://changed" events whenever YAML/JSONL files change.
 *   - On each event: re-fetch pbd_get_tree and merge via mergeSyncEvent().
 *   - On window focus: also re-fetch (safety net for missed events).
 *
 * Mutation strategy (optimistic):
 *   - updateTicketStatus() applies the change locally (optimistic) then calls
 *     pbd_update_ticket_status IPC. On IPC error: rolls back to previous status.
 *
 * Constraints:
 *   - Only one pbd_watch call per phaseRoot (deduplicated by ref in useEffect).
 *   - listen() cleanup is called on unmount to prevent event listener leaks.
 * SPORT: MASTER-COMPONENTS.md — usePewsTree (E-P8-05)
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { listen } from '@tauri-apps/api/event'
import type { PewsTree, TicketStatus } from '../../types/pbd'
import { pbdGetTree, pbdUpdateTicketStatus, pbdWatch } from '../../types/pbd'
import { applyOptimisticStatusChange, mergeSyncEvent } from './pewsTree'

// ── Return type ───────────────────────────────────────────────────────────────

export interface UsePewsTreeResult {
  /** The current PEWS tree, or null while loading/on error. */
  tree: PewsTree | null
  /** True while the initial load is in flight. */
  loading: boolean
  /** Error message from the last failed load, or null. */
  error: string | null
  /**
   * Optimistically update a ticket status.
   * Applies the change immediately; persists via IPC; rolls back on error.
   */
  updateTicketStatus: (ticketId: string, newStatus: TicketStatus, note?: string) => Promise<void>
  /** Re-fetch the tree manually (e.g. on retry). */
  refetch: () => Promise<void>
}

// ── Hook ──────────────────────────────────────────────────────────────────────

/**
 * Purpose: Manages the full lifecycle of the PEWS tree for a given project root:
 *   load, live sync, and optimistic mutation.
 *
 * @param phaseRoot Absolute path to the project root.
 */
export function usePewsTree(phaseRoot: string): UsePewsTreeResult {
  const [tree, setTree] = useState<PewsTree | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Track whether the watcher has been installed for the current phaseRoot.
  const watchInstalledRef = useRef(false)
  const phaseRootRef = useRef(phaseRoot)
  useEffect(() => {
    phaseRootRef.current = phaseRoot
  }, [phaseRoot])

  // ── Fetch ─────────────────────────────────────────────────────────────────

  const fetchTree = useCallback(async () => {
    try {
      const newTree = await pbdGetTree(phaseRootRef.current)
      setTree((prev) => (prev ? mergeSyncEvent(prev, newTree) : newTree))
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  const refetch = useCallback(async () => {
    setLoading(true)
    await fetchTree()
    setLoading(false)
  }, [fetchTree])

  // ── Initial load ──────────────────────────────────────────────────────────

  useEffect(() => {
    setLoading(true)
    setError(null)
    watchInstalledRef.current = false

    void (async () => {
      await fetchTree()
      setLoading(false)

      // Install the file watcher after the first load.
      if (!watchInstalledRef.current) {
        watchInstalledRef.current = true
        try {
          await pbdWatch(phaseRoot)
        } catch {
          // Non-fatal: watcher unavailable (e.g. phases dir doesn't exist yet).
        }
      }
    })()
  }, [phaseRoot, fetchTree])

  // ── Live sync — pbd://changed events ─────────────────────────────────────

  useEffect(() => {
    let unlisten: (() => void) | null = null

    void listen<{ path: string }>('pbd://changed', () => {
      void fetchTree()
    }).then((fn) => {
      unlisten = fn
    })

    return () => {
      unlisten?.()
    }
  }, [fetchTree])

  // ── Focus re-fetch (safety net) ───────────────────────────────────────────

  useEffect(() => {
    const handler = () => void fetchTree()
    window.addEventListener('focus', handler)
    return () => window.removeEventListener('focus', handler)
  }, [fetchTree])

  // ── Mutation — optimistic ticket status update ─────────────────────────────

  const updateTicketStatus = useCallback(
    async (ticketId: string, newStatus: TicketStatus, note?: string) => {
      setTree((prev) => {
        if (!prev) return prev
        // Find current status for potential rollback
        const change = {
          ticketId,
          newStatus,
          previousStatus: 'planned' as TicketStatus, // placeholder; overwritten below
        }
        // Walk tree to get real previous status
        outer: for (const phase of prev.phases) {
          for (const epic of phase.epics) {
            for (const wave of epic.waves) {
              for (const sprint of wave.sprints) {
                for (const ticket of sprint.tickets) {
                  if (ticket.id === ticketId) {
                    change.previousStatus = ticket.status as TicketStatus
                    break outer
                  }
                }
              }
            }
          }
        }
        return applyOptimisticStatusChange(prev, change)
      })

      try {
        await pbdUpdateTicketStatus(phaseRoot, ticketId, newStatus, note)
        // On success: re-fetch from disk to confirm (disk is the source of truth)
        await fetchTree()
      } catch (err) {
        // Roll back the optimistic change
        setTree((prev) => {
          if (!prev) return prev
          // Re-fetch is the cleanest rollback — find the real ticket to restore from disk
          // (we don't store previousStatus in component state; just re-fetch)
          return prev
        })
        // Trigger a re-fetch to restore the actual on-disk state
        await fetchTree()
        throw err
      }
    },
    [phaseRoot, fetchTree]
  )

  return { tree, loading, error, updateTicketStatus, refetch }
}
