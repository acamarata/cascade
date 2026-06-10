/**
 * Purpose: Fetch and auto-refresh cascade usage data (summary + history + ledger)
 *   from the daemon via the T-P3-E04-29 IPC commands:
 *   - `cascade_usage_summary` — aggregated period summary
 *   - `cascade_usage_history` — last N months for the chart
 *   - `cascade_usage_ledger` — per-account completion log
 * Inputs:  period — { kind: 'day' | 'week' | 'month', offset: number }
 *          accountEmail — email for ledger filtering ('' = no ledger fetch)
 * Outputs: { summary, history, ledger, loading, error, refetch }
 * Constraints:
 *   - Never throws; all errors are captured in the `error` field.
 *   - 60 s refresh interval matches the daemon's 60 s TTL (T-P3-E04-29).
 *   - If the IPC call fails (daemon not running), returns null data gracefully.
 *   - Extends the prior single-summary hook additively (backward compatible).
 * SPORT: MASTER-HOOKS.md — useUsageData, src/hooks/useUsageData.ts
 */

import { useState, useEffect, useCallback } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { UsageSummary, MonthlyUsage, AccountLedger, UsagePeriod } from '../types/ipc'

// Re-export for backward compatibility with existing consumers.
export type { UsageSummary, MonthlyUsage, AccountLedger }
export type { UsagePeriod }

// Legacy interface aliases kept for backward compat with the original hook shape.
export interface ModelUsageRow {
  model: string
  tokens_used: number
  cost_usd: number
}
export interface AccountCostEntry {
  email: string
  cost_usd: number
  last_active: string
}

export interface UseUsageDataResult {
  /** Aggregated summary for the selected period. */
  summary: UsageSummary | null
  /** Monthly history (last 6 months). */
  history: MonthlyUsage[]
  /** Per-account completion ledger. */
  ledger: AccountLedger | null
  loading: boolean
  error: string | null
  /** Manually trigger a refetch outside the 60 s interval. */
  refetch: () => void
}

/** Refresh interval in milliseconds (60 s, matching daemon cache TTL). */
const REFRESH_INTERVAL_MS = 60_000
/** Default months of history to fetch. */
const HISTORY_MONTHS = 6

/**
 * React hook for usage analytics data.
 *
 * Fetches `cascade_usage_summary`, `cascade_usage_history`, and optionally
 * `cascade_usage_ledger` from the daemon on mount and every 60 s.
 * Returns null data (never throws) if the daemon is unavailable.
 */
export function useUsageData(period: UsagePeriod, accountEmail = ''): UseUsageDataResult {
  const [summary, setSummary] = useState<UsageSummary | null>(null)
  const [history, setHistory] = useState<MonthlyUsage[]>([])
  const [ledger, setLedger] = useState<AccountLedger | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchAll = useCallback(async () => {
    setLoading(true)
    try {
      // Fetch summary and history in parallel; ledger only when an account is selected.
      const [summaryResult, historyResult] = await Promise.all([
        invoke<UsageSummary>('cascade_usage_summary', { period }),
        invoke<MonthlyUsage[]>('cascade_usage_history', {
          monthsBack: HISTORY_MONTHS,
        }),
      ])
      setSummary(summaryResult)
      setHistory(historyResult)

      if (accountEmail) {
        const ledgerResult = await invoke<AccountLedger>('cascade_usage_ledger', {
          accountEmail,
          period,
        })
        setLedger(ledgerResult)
      } else {
        setLedger(null)
      }

      setError(null)
    } catch (err) {
      // Daemon not running or command not yet registered — graceful fallback.
      setError(err instanceof Error ? err.message : String(err))
      setSummary(null)
      setHistory([])
      setLedger(null)
    } finally {
      setLoading(false)
    }
  }, [period.kind, period.offset, accountEmail]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    fetchAll()
    const id = setInterval(fetchAll, REFRESH_INTERVAL_MS)
    return () => clearInterval(id)
  }, [fetchAll])

  return { summary, history, ledger, loading, error, refetch: fetchAll }
}
