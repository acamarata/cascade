/**
 * useProviderConnected
 *
 * Purpose: Reads the live AI provider connection state from the daemon
 *   via the cascade_providers_health Tauri IPC command. Returns
 *   `{ anyConnected, connectedProviders }` for use by AIGatedStep and
 *   the wizard flow state machine.
 *
 * Inputs: none (subscribes on mount; re-polls on focus by default)
 *
 * Outputs: { anyConnected: boolean, connectedProviders: string[] }
 *   - anyConnected: true when at least one provider is connected.
 *   - connectedProviders: list of connected provider ids.
 *   Falls back to { anyConnected: false, connectedProviders: [] } if
 *   the IPC call fails (daemon not running).
 *
 * Constraints:
 *   - T-P3-E03-42: AI-Optional Core — this hook must never throw;
 *     all errors resolve to anyConnected=false.
 *   - cascade_providers_health IPC is registered in lib.rs (W-05 scaffold).
 *   - FILL T-P3-E04-25: real provider health polling; current stub returns [].
 *
 * SPORT: MASTER-HOOKS.md — useProviderConnected
 */

import { useState, useEffect, useCallback } from 'react'
import { invoke } from '@tauri-apps/api/core'

export interface ProviderConnectionState {
  anyConnected: boolean
  connectedProviders: string[]
}

/**
 * React hook that returns the current AI provider connection state.
 *
 * Polls cascade_providers_health on mount and re-polls on window focus
 * (so the UI updates after the user connects a provider in another window).
 *
 * Returns false/empty-list if the IPC call fails (daemon not running,
 * no providers configured). Never throws.
 */
export function useProviderConnected(): ProviderConnectionState {
  const [state, setState] = useState<ProviderConnectionState>({
    anyConnected: false,
    connectedProviders: [],
  })

  const fetchProviderHealth = useCallback(async () => {
    try {
      // T-P3-E04-25: cascade_providers_health returns string[] of connected provider ids.
      // Currently returns [] (stub) — gracefully handled as anyConnected=false.
      const providers = await invoke<string[]>('cascade_providers_health')
      setState({
        anyConnected: providers.length > 0,
        connectedProviders: providers,
      })
    } catch {
      // Daemon not running or no providers configured — AI-optional fallback.
      // This is the expected path during onboarding before daemon is installed.
      setState({ anyConnected: false, connectedProviders: [] })
    }
  }, [])

  useEffect(() => {
    // Initial fetch on mount
    fetchProviderHealth()

    // Re-poll on focus: user may connect a provider in another window/tab
    window.addEventListener('focus', fetchProviderHealth)
    return () => {
      window.removeEventListener('focus', fetchProviderHealth)
    }
  }, [fetchProviderHealth])

  return state
}
