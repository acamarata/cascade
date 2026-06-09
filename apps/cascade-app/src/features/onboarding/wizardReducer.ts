/**
 * Purpose: useReducer implementation for wizard state transitions.
 * Inputs: Current state + dispatched actions (discriminated union).
 * Outputs: Next state after applying action logic.
 * Constraints: All transitions synchronous. Boundary checks on step navigation.
 *   No async; file persistence handled by context consumer.
 * SPORT: E5-S01-02 — wizardReducer state machine
 */

import type { WizardState } from './types'
import { WizardStep } from './types'

/**
 * Discriminated union of all possible wizard actions.
 * Each action has a unique 'type' discriminant.
 *
 * UPDATE_ARCHIVE_STATUS: dispatched by ArchiveLegacyPhase (T-P3-E03-20) after
 * each successful archive_legacy_tools call. Updates archivedTools map so
 * downstream phases (symlink setup) know which tools were archived.
 * Task: T-P3-E03-21
 */
export type WizardAction =
  | { type: 'NEXT' }
  | { type: 'BACK' }
  | { type: 'JUMP_TO'; payload: WizardStep }
  | { type: 'MARK_COMPLETE'; payload: WizardStep }
  | { type: 'UPDATE_STATE'; payload: Partial<WizardState> }
  | { type: 'UPDATE_ARCHIVE_STATUS'; payload: { toolId: string; archived: boolean } }

/**
 * wizardReducer: Pure state reducer for wizard navigation and status updates.
 *
 * @param state Current wizard state
 * @param action One of: NEXT, BACK, JUMP_TO, MARK_COMPLETE, UPDATE_STATE
 * @returns Next state after applying the action
 *
 * Constraints:
 * - goBack() at step 1 leaves step unchanged (no underflow)
 * - goNext() at step 10 (Done) leaves step unchanged (no overflow)
 * - jumpTo() sets currentStep unconditionally (if payload is a valid step)
 * - MARK_COMPLETE adds to completedSteps and advances to next step
 * - UPDATE_STATE merges shallow partial into current state
 */
export function wizardReducer(state: WizardState, action: WizardAction): WizardState {
  const now = new Date().toISOString()

  switch (action.type) {
    case 'NEXT': {
      // Advance to next step, capped at step 10 (Done)
      const nextStep = Math.min(state.step + 1, WizardStep.Done) as WizardStep
      return {
        ...state,
        step: nextStep,
        updatedAt: now,
      }
    }

    case 'BACK': {
      // Go to previous step, capped at step 1 (Welcome)
      const prevStep = Math.max(state.step - 1, WizardStep.Welcome) as WizardStep
      return {
        ...state,
        step: prevStep,
        updatedAt: now,
      }
    }

    case 'JUMP_TO': {
      // Jump directly to a specific step
      const targetStep = action.payload
      // Validate target is a valid WizardStep (1-10)
      if (targetStep < WizardStep.Welcome || targetStep > WizardStep.Done) {
        return state // Invalid jump target, no change
      }
      return {
        ...state,
        step: targetStep,
        updatedAt: now,
      }
    }

    case 'MARK_COMPLETE': {
      // Mark a step as complete and move to next step
      const stepToComplete = action.payload
      const updated = new Set(state.completedSteps)
      updated.add(stepToComplete)
      const nextStep = Math.min(stepToComplete + 1, WizardStep.Done) as WizardStep
      return {
        ...state,
        step: nextStep,
        completedSteps: updated,
        updatedAt: now,
      }
    }

    case 'UPDATE_STATE': {
      // Merge partial state update
      const updated: WizardState = {
        ...state,
        ...action.payload,
        updatedAt: now,
      }
      // Preserve completedSteps set if not explicitly overridden
      if (action.payload.completedSteps === undefined) {
        updated.completedSteps = state.completedSteps
      }
      return updated
    }

    case 'UPDATE_ARCHIVE_STATUS': {
      // Update archivedTools map for a single tool.
      // Called by ArchiveLegacyPhase after each archive_legacy_tools success.
      // Task: T-P3-E03-21
      const { toolId, archived } = action.payload
      return {
        ...state,
        archivedTools: {
          ...state.archivedTools,
          [toolId]: archived,
        },
        updatedAt: now,
      }
    }

    default: {
      const _exhaustive: never = action
      return _exhaustive
    }
  }
}
