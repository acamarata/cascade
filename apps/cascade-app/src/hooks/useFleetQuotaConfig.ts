/**
 * Purpose: Manage user-configurable per-model token quota estimates, persisted to
 *   localStorage. Falls back to static MODEL_QUOTA_ESTIMATE defaults when no user
 *   config is present.
 * Inputs:  localStorage key 'cascade:fleet:quota-estimates'.
 * Outputs: { estimates, setEstimate, reset } + useEffectiveQuotaEstimates convenience hook.
 * Constraints:
 *   - No IPC/network — localStorage only.
 *   - Falls back to MODEL_QUOTA_ESTIMATE defaults on parse error.
 *   - setEstimate updates one entry; reset clears to defaults.
 * SPORT: MASTER-HOOKS.md — useFleetQuotaConfig
 */

import { useCallback, useEffect, useState } from 'react'
import { MODEL_QUOTA_ESTIMATE } from '../components/widget/QuotaGaugeRow'

const LS_KEY = 'cascade:fleet:quota-estimates'

function loadFromStorage(): Record<string, number> | null {
  try {
    const raw = localStorage.getItem(LS_KEY)
    if (!raw) return null
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
    const result: Record<string, number> = {}
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v === 'number') result[k] = v
    }
    return result
  } catch {
    return null
  }
}

export interface UseFleetQuotaConfigResult {
  estimates: Record<string, number>
  setEstimate: (model: string, tokens: number) => void
  reset: () => void
}

/**
 * Hook for per-model quota estimate config.
 * Persists overrides to localStorage; merges with static defaults on load.
 */
export function useFleetQuotaConfig(): UseFleetQuotaConfigResult {
  const [overrides, setOverrides] = useState<Record<string, number>>({})

  useEffect(() => {
    const saved = loadFromStorage()
    if (saved) setOverrides(saved)
  }, [])

  const estimates: Record<string, number> = {
    ...MODEL_QUOTA_ESTIMATE,
    ...overrides,
  }

  const setEstimate = useCallback((model: string, tokens: number) => {
    setOverrides((prev) => {
      const next = { ...prev, [model]: tokens }
      localStorage.setItem(LS_KEY, JSON.stringify(next))
      return next
    })
  }, [])

  const reset = useCallback(() => {
    localStorage.removeItem(LS_KEY)
    setOverrides({})
  }, [])

  return { estimates, setEstimate, reset }
}

/**
 * Convenience hook returning the effective quota estimates (defaults merged with
 * user overrides). Components that only need to read estimates can use this
 * instead of the full useFleetQuotaConfig.
 */
export function useEffectiveQuotaEstimates(): Record<string, number> {
  const { estimates } = useFleetQuotaConfig()
  return estimates
}
