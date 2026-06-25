/**
 * Purpose: Fetch + auto-poll the accounts quota snapshot from the daemon-written
 *   quota.json via the `accounts_list` Tauri command (Epic 1, T1.2).
 * Inputs:  None.
 * Outputs: { accounts, loading, error, refetch }.
 * Constraints:
 *   - Never throws; IPC errors are captured in `error`.
 *   - Polls every 30 s.
 *   - First load shows loading; subsequent polls refresh silently.
 * SPORT: MASTER-HOOKS.md — useAccounts, src/features/accounts/useAccounts.ts
 */

import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { AccountQuota, AccountsQuota } from './types'

/** Poll interval in milliseconds. */
const POLL_INTERVAL_MS = 30_000

export interface UseAccountsResult {
  accounts: AccountQuota[]
  loading: boolean
  error: string | null
  refetch: () => Promise<void>
}

/**
 * React hook for the accounts quota snapshot.
 * Fetches on mount and every 30 s; returns empty data (never throws) on error.
 */
export function useAccounts(): UseAccountsResult {
  const [accounts, setAccounts] = useState<AccountQuota[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchAll = useCallback(async () => {
    try {
      const result = await invoke<AccountsQuota>('accounts_list')
      setAccounts(result.accounts ?? [])
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setAccounts([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void fetchAll()
    const id = setInterval(() => {
      void fetchAll()
    }, POLL_INTERVAL_MS)
    return () => clearInterval(id)
  }, [fetchAll])

  return { accounts, loading, error, refetch: fetchAll }
}
