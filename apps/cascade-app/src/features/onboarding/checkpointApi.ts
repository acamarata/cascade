/**
 * Purpose: TypeScript bindings for wizard checkpoint persistence commands.
 * Inputs: Tauri invoke API, WizardCheckpoint type from types.ts.
 * Outputs: Typed async functions for save, load, clear checkpoint operations.
 * Constraints: Wraps Tauri IPC errors in typed results; handles serialization round-trip.
 * SPORT: MASTER-COMPONENTS.md — checkpointApi module
 */

import { invoke } from '@tauri-apps/api/core'
import { WizardCheckpoint } from './types'

/**
 * Save a wizard checkpoint to ~/.cascade/wizard-state.json.
 *
 * # Atomicity
 * The Rust side writes to a temporary file and atomically renames it,
 * guaranteeing that the checkpoint file is never partially written.
 *
 * # Arguments
 * - `checkpoint`: The checkpoint state to persist
 *
 * # Returns
 * - `Promise<void>` on success
 * - Rejects with an error string if the write fails
 */
export async function saveCheckpoint(checkpoint: WizardCheckpoint): Promise<void> {
  try {
    await invoke<void>('wizard_save_checkpoint', { checkpoint })
  } catch (error) {
    throw new Error(
      `Failed to save checkpoint: ${error instanceof Error ? error.message : String(error)}`
    )
  }
}

/**
 * Load a wizard checkpoint from ~/.cascade/wizard-state.json.
 *
 * # Returns
 * - `Promise<WizardCheckpoint | null>` if the file exists and is valid JSON
 * - `null` if the checkpoint file does not exist
 * - Rejects with an error string if deserialization fails
 */
export async function loadCheckpoint(): Promise<WizardCheckpoint | null> {
  try {
    return await invoke<WizardCheckpoint | null>('wizard_load_checkpoint')
  } catch (error) {
    throw new Error(
      `Failed to load checkpoint: ${error instanceof Error ? error.message : String(error)}`
    )
  }
}

/**
 * Clear (delete) the wizard checkpoint file.
 *
 * Called when the wizard completes (step 10, Done). After this, the next
 * wizard session will start fresh.
 *
 * Succeeds even if the file does not exist (idempotent operation).
 *
 * # Returns
 * - `Promise<void>` on success or if the file doesn't exist
 * - Rejects with an error string if deletion fails
 */
export async function clearCheckpoint(): Promise<void> {
  try {
    await invoke<void>('wizard_clear_checkpoint')
  } catch (error) {
    throw new Error(
      `Failed to clear checkpoint: ${error instanceof Error ? error.message : String(error)}`
    )
  }
}
