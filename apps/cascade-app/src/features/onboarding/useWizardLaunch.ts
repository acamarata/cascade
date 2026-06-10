/**
 * Purpose: Hook for first-run detection and wizard launch routing.
 * Inputs:  None (reads from Tauri backend).
 * Outputs: { status, isLoading, error } — routing info for App to decide: launch wizard or show main app.
 * Constraints: Must not cause infinite redirect loops. Must handle all 3 WizardStatus variants.
 *   Called once on App mount; cleanup on unmount.
 * SPORT: MASTER-COMPONENTS.md — useWizardLaunch
 */

import { useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { WizardStatus } from './types'

/**
 * Hook for first-run detection and wizard launch routing.
 *
 * On app mount, calls the Tauri `check_wizard_status` command to determine if:
 * - Wizard should launch (NeverRun or InProgress)
 * - Wizard should be skipped (Complete)
 *
 * The App component uses this hook to route to /onboarding or / (main) accordingly.
 *
 * @returns Object with:
 * - `status`: WizardStatus enum variant (NeverRun | InProgress | Complete), or null if loading
 * - `isLoading`: true while checking status
 * - `error`: error string if check failed
 *
 * @example
 * ```tsx
 * function App() {
 *   const { status, isLoading } = useWizardLaunch()
 *   if (isLoading) return <LoadingScreen />
 *   if (status && 'NeverRun' in status) return <Navigate to="/onboarding" replace />
 *   if (status && 'InProgress' in status) return <Navigate to="/onboarding" replace />
 *   return <MainApp /> // Complete status
 * }
 * ```
 */
export function useWizardLaunch() {
  const [status, setStatus] = useState<WizardStatus | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let isMounted = true

    async function checkStatus() {
      try {
        const result = await invoke<WizardStatus>('check_wizard_status')
        if (isMounted) {
          setStatus(result)
          setError(null)
        }
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err)
        if (isMounted) {
          setError(message)
          // Default to showing main app if check fails (graceful degradation)
          setStatus({ Complete: true } as WizardStatus)
        }
      } finally {
        if (isMounted) {
          setIsLoading(false)
        }
      }
    }

    checkStatus()

    return () => {
      isMounted = false
    }
  }, [])

  return { status, isLoading, error }
}
