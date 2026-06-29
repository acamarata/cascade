/**
 * Purpose: React hook that bridges the cascade-personal encrypted vault IPC
 *   commands into typed React state. Manages open/list/query/upsert/exposure
 *   lifecycle for the personal data vault.
 * Inputs:  None (self-contained; reads Tauri IPC on mount).
 * Outputs: { collections, records, exposureLog, selectedCollection, mode,
 *            loading, error, setMode, selectCollection, addRecord,
 *            refreshCollections }
 * Constraints:
 *   - Requires Tauri runtime; gracefully degrades to empty state in browser/Vitest.
 *   - open_vault_cmd must succeed before any other command is called.
 *   - Records and exposure log are scoped to selectedCollection; both are empty
 *     while no collection is selected.
 *   - mode changes trigger an automatic re-query when a collection is selected.
 * SPORT: MASTER-COMPONENTS.md — usePersonalEncryptedVault, rag-16-ui
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'

// ---------------------------------------------------------------------------
// Exported types
// ---------------------------------------------------------------------------

export interface CollectionInfo {
  name: string
  label: string
  sensitivity: string
}

export interface VaultRecord {
  id: string
  collection: string
  payload: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface ExposureEntry {
  id: number
  collection: string
  item_id: string | null
  exposed_to: string
  granted_by: string
  exposed_at: string
}

export type CascadeMode = 'normal' | 'personal'

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

const isTauri =
  typeof window !== 'undefined' && '__TAURI__' in window

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export function usePersonalEncryptedVault(): {
  collections: CollectionInfo[]
  records: VaultRecord[]
  exposureLog: ExposureEntry[]
  selectedCollection: string | null
  mode: CascadeMode
  loading: boolean
  error: string | null
  setMode: (m: CascadeMode) => void
  selectCollection: (name: string) => void
  addRecord: (collection: string, payload: Record<string, unknown>) => Promise<string>
  refreshCollections: () => Promise<void>
} {
  const [collections, setCollections] = useState<CollectionInfo[]>([])
  const [records, setRecords] = useState<VaultRecord[]>([])
  const [exposureLog, setExposureLog] = useState<ExposureEntry[]>([])
  const [selectedCollection, setSelectedCollection] = useState<string | null>(null)
  const [mode, setMode] = useState<CascadeMode>('normal')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Track whether the vault has been opened so we don't double-open.
  const vaultOpened = useRef(false)

  // ------------------------------------------------------------------
  // Open vault + initial collection load
  // ------------------------------------------------------------------

  const refreshCollections = useCallback(async () => {
    if (!isTauri) return
    setLoading(true)
    setError(null)
    try {
      if (!vaultOpened.current) {
        await invoke<string>('open_vault_cmd')
        vaultOpened.current = true
      }
      const cols = await invoke<CollectionInfo[]>('list_collections_cmd')
      setCollections(cols)
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      setError(`Vault unavailable: ${msg}`)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refreshCollections()
  }, [refreshCollections])

  // ------------------------------------------------------------------
  // Query records + exposure log when selection or mode changes
  // ------------------------------------------------------------------

  useEffect(() => {
    if (!isTauri || selectedCollection === null) {
      setRecords([])
      setExposureLog([])
      return
    }

    let cancelled = false
    ;(async () => {
      setLoading(true)
      setError(null)
      try {
        const [recs, log] = await Promise.all([
          invoke<VaultRecord[]>('query_records_cmd', {
            collection: selectedCollection,
            mode,
          }),
          invoke<ExposureEntry[]>('exposure_log_cmd', {
            collection: selectedCollection,
          }),
        ])
        if (!cancelled) {
          setRecords(recs)
          setExposureLog(log)
        }
      } catch (err: unknown) {
        if (!cancelled) {
          const msg = err instanceof Error ? err.message : String(err)
          setError(`Query failed: ${msg}`)
          setRecords([])
          setExposureLog([])
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()

    return () => {
      cancelled = true
    }
  }, [selectedCollection, mode])

  // ------------------------------------------------------------------
  // selectCollection
  // ------------------------------------------------------------------

  const selectCollection = useCallback((name: string) => {
    setSelectedCollection(name)
  }, [])

  // ------------------------------------------------------------------
  // addRecord
  // ------------------------------------------------------------------

  const addRecord = useCallback(
    async (collection: string, payload: Record<string, unknown>): Promise<string> => {
      if (!isTauri) throw new Error('Not in Tauri runtime')
      const id = await invoke<string>('upsert_record_cmd', {
        collection,
        id: null,
        payload,
      })
      // Re-fetch records for the collection that was just written.
      if (collection === selectedCollection) {
        try {
          const recs = await invoke<VaultRecord[]>('query_records_cmd', {
            collection,
            mode,
          })
          setRecords(recs)
        } catch {
          // Non-fatal — the record was upserted; stale list is acceptable.
        }
      }
      return id
    },
    [selectedCollection, mode],
  )

  // ------------------------------------------------------------------
  // setMode (wrapped to avoid exposing raw setter)
  // ------------------------------------------------------------------

  const handleSetMode = useCallback((m: CascadeMode) => {
    setMode(m)
  }, [])

  return {
    collections,
    records,
    exposureLog,
    selectedCollection,
    mode,
    loading,
    error,
    setMode: handleSetMode,
    selectCollection,
    addRecord,
    refreshCollections,
  }
}
