/**
 * Purpose: React hook for wizard checkpoint auto-persistence.
 * Inputs: WizardContext state, debounce interval (500ms).
 * Outputs: Async load/save/clear functions; auto-saves on context state changes.
 * Constraints: Must be called within WizardProvider; debounces disk writes to avoid flooding.
 * SPORT: MASTER-COMPONENTS.md — useCheckpoint hook
 */

import { useEffect, useCallback, useRef } from 'react'
import { useWizard } from './WizardContext'
import { saveCheckpoint, loadCheckpoint, clearCheckpoint } from './checkpointApi'
import { type WizardCheckpoint, checkpointToState } from './types'

/**
 * Auto-save debounce timeout (milliseconds).
 * Prevents flooding the disk on rapid wizard state changes.
 */
const AUTO_SAVE_DEBOUNCE_MS = 500

/**
 * useCheckpoint hook: auto-persistence for the wizard context.
 *
 * Automatically saves the wizard state to ~/.cascade/wizard-state.json whenever
 * the WizardContext changes, with a 500ms debounce to avoid excessive disk writes.
 * On mount, attempts to load and restore a saved checkpoint if one exists.
 *
 * @returns Object with load(), save(), clear() async functions.
 * @throws Error if called outside WizardProvider.
 *
 * @example
 * ```tsx
 * const { load, save, clear } = useCheckpoint()
 * useEffect(() => { load() }, [load])
 * ```
 */
export function useCheckpoint() {
  const wizard = useWizard()
  const { state, updateState } = wizard

  // Use a ref to track the debounce timeout so we can cancel it
  const debounceTimeoutRef = useRef<NodeJS.Timeout | null>(null)

  /**
   * Load the checkpoint from disk and restore wizard context state.
   *
   * Converts the serialized WizardCheckpoint (camelCase fields, number[] completedSteps)
   * back into a WizardState and dispatches it via updateState so the wizard resumes
   * at the saved step. Silently returns if no checkpoint exists (first-run scenario).
   *
   * Field expectations (Rust side emits camelCase):
   *   legacyConfigPath, fileCount, totalSizeBytes, manifestPath, etc.
   */
  const load = useCallback(async (): Promise<void> => {
    try {
      const checkpoint = await loadCheckpoint()
      if (!checkpoint) {
        // No saved checkpoint; wizard starts fresh
        return
      }
      // Restore state: convert checkpoint back to WizardState and dispatch
      const restoredState = checkpointToState(checkpoint)
      updateState(restoredState)
    } catch (error) {
      console.error('Failed to load checkpoint:', error)
      // Non-fatal; proceed with fresh wizard
    }
  }, [updateState])

  /**
   * Save the current wizard context to disk atomically.
   */
  const save = useCallback(async (): Promise<void> => {
    try {
      const checkpoint: WizardCheckpoint = {
        step: state.step,
        completedSteps: Array.from(state.completedSteps),
        // T-P3-E03-42: persist skipped steps so wizard resumes correctly after app close.
        skippedSteps: Array.from(state.skippedSteps),
        providerConnected: state.providerConnected,
        scanResult: state.scanResult,
        detectedToolIds: state.detectedToolIds,
        mergeResult: state.mergeResult,
        // T-P3-E03-33: persist per-tier AI merge results so phase 4 can resume after app close.
        // RichMergeResult is JSON-safe — sections carry status + editedContent inline.
        mergeResults: state.mergeResults,
        toolModes: state.toolModes,
        archiveManifestPath: state.archiveManifestPath,
        archivedTools: state.archivedTools,
        symlinkPlanApplied: state.symlinkPlanApplied,
        daemonInstalled: state.daemonInstalled,
        startedAt: state.startedAt,
        updatedAt: state.updatedAt,
      }
      await saveCheckpoint(checkpoint)
    } catch (error) {
      console.error('Failed to save checkpoint:', error)
      // Non-fatal; user can retry
    }
  }, [state])

  /**
   * Clear the checkpoint file (called when wizard reaches Done step).
   */
  const clear = useCallback(async (): Promise<void> => {
    try {
      await clearCheckpoint()
    } catch (error) {
      console.error('Failed to clear checkpoint:', error)
      // Non-fatal
    }
  }, [])

  /**
   * Auto-save on every context state change, debounced.
   * Cancels pending saves if new changes arrive.
   */
  useEffect(() => {
    if (debounceTimeoutRef.current) {
      clearTimeout(debounceTimeoutRef.current)
    }

    debounceTimeoutRef.current = setTimeout(() => {
      save().catch((err) => console.error('Auto-save failed:', err))
    }, AUTO_SAVE_DEBOUNCE_MS)

    return () => {
      if (debounceTimeoutRef.current) {
        clearTimeout(debounceTimeoutRef.current)
      }
    }
  }, [state, save])

  return { load, save, clear }
}
