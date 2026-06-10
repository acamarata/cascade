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
import type { MergeResult as RichMergeResult, SectionStatus } from './merge/types'

/**
 * Discriminated union of all possible wizard actions.
 * Each action has a unique 'type' discriminant.
 *
 * UPDATE_ARCHIVE_STATUS: dispatched by ArchiveLegacyPhase (T-P3-E03-20) after
 * each successful archive_legacy_tools call. Updates archivedTools map so
 * downstream phases (symlink setup) know which tools were archived.
 * Task: T-P3-E03-21
 *
 * SET_MERGE_RESULT: stores (or replaces) the full AI merge result for a given tier id.
 * Called by mergeService.mergeForTier() after each tier completes (T-P3-E03-27).
 * Supports incremental streaming: each section can be upserted without full replacement.
 *
 * UPDATE_SECTION_STATUS: updates the status (and optionally editedContent) of a single
 * section within a tier's MergeResult. Used by approve/reject/edit section actions.
 * Immutable: creates new section objects, never mutates existing ones.
 * T-P3-E03-27 constraint: editedContent may only be set when status === 'edited'.
 */
export type WizardAction =
  | { type: 'NEXT' }
  | { type: 'BACK' }
  | { type: 'JUMP_TO'; payload: WizardStep }
  | { type: 'MARK_COMPLETE'; payload: WizardStep }
  /**
   * SKIP_STEP: marks a wizard step as skipped (AI-optional gate, T-P3-E03-42).
   * Records the step in skippedSteps (persisted as wizard-state.json) and advances
   * to the next step. Skipped steps do not block phase completion.
   */
  | { type: 'SKIP_STEP'; payload: WizardStep }
  | { type: 'UPDATE_STATE'; payload: Partial<WizardState> }
  | { type: 'UPDATE_ARCHIVE_STATUS'; payload: { toolId: string; archived: boolean } }
  | { type: 'SET_MERGE_RESULT'; payload: { tier: string; result: RichMergeResult } }
  /**
   * SET_SELECTED_TEMPLATES: stores the user's template picker selection from Phase 4.
   * T-P3-E05-19: called by TemplatePickerCompact onChange handler via MergeContentPhase.
   */
  | { type: 'SET_SELECTED_TEMPLATES'; payload: string[] }
  | {
      type: 'UPDATE_SECTION_STATUS'
      payload: {
        tier: string
        sectionId: string
        status: SectionStatus
        editedContent?: string
      }
    }

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

    case 'SKIP_STEP': {
      // Mark a step as skipped (AI-optional, T-P3-E03-42) and advance.
      // Step is recorded in skippedSteps (persisted as status='skipped').
      // Skipped steps do not block phase completion — wizard advances normally.
      const stepToSkip = action.payload
      const updatedSkipped = new Set(state.skippedSteps)
      updatedSkipped.add(stepToSkip)
      const nextStep = Math.min(stepToSkip + 1, WizardStep.Done) as WizardStep
      return {
        ...state,
        step: nextStep,
        skippedSteps: updatedSkipped,
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

    case 'SET_MERGE_RESULT': {
      // Store or replace the AI merge result for a tier.
      // Keyed by tier id string ('global', 'project', etc.).
      // T-P3-E03-27: called by mergeService after each tier completes.
      const { tier, result } = action.payload
      return {
        ...state,
        mergeResults: {
          ...state.mergeResults,
          [tier]: result,
        },
        updatedAt: now,
      }
    }

    case 'SET_SELECTED_TEMPLATES': {
      // Store the user's template selection from the Phase 4 picker. T-P3-E05-19.
      return {
        ...state,
        selectedTemplates: action.payload,
        updatedAt: now,
      }
    }

    case 'UPDATE_SECTION_STATUS': {
      // Immutable update of a single section's status within a tier's MergeResult.
      // T-P3-E03-27: editedContent constraint — only set when status === 'edited'.
      const { tier, sectionId, status, editedContent } = action.payload
      const tierResult = state.mergeResults[tier]
      if (!tierResult) {
        // No merge result for this tier yet — no-op
        return state
      }
      const updatedSections = tierResult.sections.map((section) => {
        if (section.id !== sectionId) return section
        // Build new section immutably
        const updated = { ...section, status }
        if (status === 'edited' && editedContent !== undefined) {
          updated.editedContent = editedContent
        } else if (status !== 'edited') {
          // Clear editedContent when reverting away from 'edited'
          delete updated.editedContent
        }
        return updated
      })
      return {
        ...state,
        mergeResults: {
          ...state.mergeResults,
          [tier]: {
            ...tierResult,
            sections: updatedSections,
          },
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
