/**
 * Purpose: Fetch + auto-poll the accounts quota snapshot straight from the
 *   daemon-written quota.json via the `accounts_list` Tauri command (Epic 1,
 *   T1.2). `accounts_list` reads the file directly — it does NOT go through
 *   the daemon JSON-RPC health ping, so this hook (and everything downstream
 *   of it) must never be gated on `useDaemonStatus`.
 * Inputs:  None.
 * Outputs: { accounts, needsAuth, isLive, lastUpdated, loading, error, refetch }.
 * Constraints:
 *   - Never throws; IPC errors are captured in `error`.
 *   - Polls every 4 s — same cadence class as the macOS menu-bar widget, so
 *     the desktop app never lags a fresh quota.json (the daemon writes it
 *     roughly every 60 s, but re-auth/refresh actions can update it any time).
 *   - `accounts` is filtered to authenticated accounts only (see
 *     `isAuthenticated` — absent `authenticated` defaults to true so the UI
 *     doesn't hide everything mid-rollout of the field). Non-authenticated
 *     rows are exposed separately via `needsAuth` for a "Needs sign-in" row.
 *   - `isLive`/`lastUpdated` are derived from the freshest `last_pull_at`
 *     across all accounts — this is the "is the store fresh" signal the UI
 *     should use instead of a daemon health ping.
 *   - First load shows loading; subsequent polls refresh silently.
 * SPORT: MASTER-HOOKS.md — useAccounts, src/features/accounts/useAccounts.ts
 */

import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { isAuthenticated, type AccountQuota, type AccountsQuota } from './types'

/** Poll interval in milliseconds — fast enough that the app never shows stale numbers. */
const POLL_INTERVAL_MS = 4_000

/**
 * How stale the freshest `last_pull_at` can be before we consider the store
 * no longer "live". The daemon writes quota.json roughly every 60 s; 3x that
 * comfortably absorbs a couple of missed cycles without flapping.
 */
const FRESHNESS_THRESHOLD_MS = 180_000

export interface UseAccountsResult {
  /** Authenticated accounts only — safe to render utilization for. */
  accounts: AccountQuota[]
  /** Accounts present in the roster but not currently authenticated. */
  needsAuth: AccountQuota[]
  /** True when quota.json's freshest pull is within the freshness threshold. */
  isLive: boolean
  /** Unix ms of the freshest `last_pull_at` across all accounts, or null. */
  lastUpdated: number | null
  loading: boolean
  error: string | null
  refetch: () => Promise<void>
}

/** Freshest `last_pull_at` (seconds) across all accounts, converted to unix ms. */
function freshestPullAt(accounts: AccountQuota[]): number | null {
  let freshest: number | null = null
  for (const acc of accounts) {
    if (acc.last_pull_at == null) continue
    const ms = acc.last_pull_at * 1000
    if (freshest == null || ms > freshest) freshest = ms
  }
  return freshest
}

/**
 * React hook for the accounts quota snapshot.
 * Fetches on mount and every 4 s; returns empty data (never throws) on error.
 * Never depends on daemon connection status — `accounts_list` reads the
 * quota.json file directly and must keep working even if the daemon's
 * JSON-RPC health ping is down.
 */
export function useAccounts(): UseAccountsResult {
  const [allAccounts, setAllAccounts] = useState<AccountQuota[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchAll = useCallback(async () => {
    try {
      const result = await invoke<AccountsQuota>('accounts_list')
      setAllAccounts(result.accounts ?? [])
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setAllAccounts([])
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

  const accounts = allAccounts.filter(isAuthenticated)
  const needsAuth = allAccounts.filter((acc) => !isAuthenticated(acc))
  const lastUpdated = freshestPullAt(allAccounts)
  const isLive = lastUpdated != null && Date.now() - lastUpdated < FRESHNESS_THRESHOLD_MS

  return { accounts, needsAuth, isLive, lastUpdated, loading, error, refetch: fetchAll }
}
