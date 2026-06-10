/**
 * Purpose: React context for wizard state management and navigation.
 * Inputs: WizardProvider wraps children; useWizard() hook consumed by wizard UI.
 * Outputs: Typed hook exposing current state, navigation, and completion tracking.
 * Constraints: useWizard must be called within WizardProvider (throws if outside).
 *   State changes are synchronous; checkpoint persistence is caller's responsibility.
 * SPORT: E5-S01-02 — WizardContext + useWizard hook
 *   T-P3-E03-27: mergeResults helpers (getMergeResult, approveSection, rejectSection,
 *     editSection, setMergeResult) added to context value and useWizard() hook.
 */

import {
  createContext,
  useContext,
  useReducer,
  ReactNode,
  useCallback,
} from 'react'
import type { WizardState } from './types'
import { WizardStep, createInitialState } from './types'
import { wizardReducer, type WizardAction } from './wizardReducer'
import type { MergeResult as RichMergeResult } from './merge/types'

/**
 * WizardContextValue: The shape of data exposed by the context.
 *
 * Merge section helpers (T-P3-E03-27):
 *   getMergeResult(tier)         — retrieve the full MergeResult for a tier (undefined if absent)
 *   setMergeResult(tier, result) — store/replace the AI merge result for a tier
 *   approveSection(tier, id)     — set section status to 'approved'
 *   rejectSection(tier, id)      — set section status to 'rejected'
 *   editSection(tier, id, text)  — set section status to 'edited' and store editedContent
 */
interface WizardContextValue {
  state: WizardState
  dispatch: (action: WizardAction) => void
  currentStep: WizardStep
  completedSteps: Set<WizardStep>
  /** Steps the user explicitly skipped (AI-optional gate). T-P3-E03-42. */
  skippedSteps: Set<WizardStep>
  goNext: () => void
  goBack: () => void
  jumpTo: (step: WizardStep) => void
  markComplete: (step: WizardStep) => void
  /**
   * skipStep: marks a step as skipped and advances to next step.
   * T-P3-E03-42: used by AIGatedStep "Skip for now" button.
   */
  skipStep: (step: WizardStep) => void
  updateState: (partial: Partial<WizardState>) => void
  // Merge section helpers — T-P3-E03-27
  getMergeResult: (tier: string) => RichMergeResult | undefined
  setMergeResult: (tier: string, result: RichMergeResult) => void
  approveSection: (tier: string, sectionId: string) => void
  rejectSection: (tier: string, sectionId: string) => void
  editSection: (tier: string, sectionId: string, content: string) => void
}

/**
 * WizardContext: Internal context created with undefined default.
 * useWizard() will throw if called outside WizardProvider.
 */
const WizardContext = createContext<WizardContextValue | undefined>(undefined)

/**
 * WizardProvider: Context provider that wraps wizard routes.
 * Initializes wizard state and exposes navigation/state mutations.
 *
 * @param props.children React children to wrap
 */
export function WizardProvider({ children }: { children: ReactNode }) {
  const [state, dispatch] = useReducer(wizardReducer, undefined, createInitialState)

  // Memoized navigation helpers
  const goNext = useCallback(() => {
    dispatch({ type: 'NEXT' })
  }, [])

  const goBack = useCallback(() => {
    dispatch({ type: 'BACK' })
  }, [])

  const jumpTo = useCallback((step: WizardStep) => {
    dispatch({ type: 'JUMP_TO', payload: step })
  }, [])

  const markComplete = useCallback((step: WizardStep) => {
    dispatch({ type: 'MARK_COMPLETE', payload: step })
  }, [])

  const skipStep = useCallback((step: WizardStep) => {
    dispatch({ type: 'SKIP_STEP', payload: step })
  }, [])

  const updateState = useCallback((partial: Partial<WizardState>) => {
    dispatch({ type: 'UPDATE_STATE', payload: partial })
  }, [])

  // Merge section helpers — T-P3-E03-27

  /** Retrieve the AI merge result for a tier by tier id string ('global', 'project', etc.). */
  const getMergeResult = useCallback(
    (tier: string): RichMergeResult | undefined => state.mergeResults[tier],
    [state.mergeResults],
  )

  /** Store or replace the full AI merge result for a tier. */
  const setMergeResult = useCallback(
    (tier: string, result: RichMergeResult) => {
      dispatch({ type: 'SET_MERGE_RESULT', payload: { tier, result } })
    },
    [],
  )

  /** Set a section's status to 'approved'. */
  const approveSection = useCallback(
    (tier: string, sectionId: string) => {
      dispatch({
        type: 'UPDATE_SECTION_STATUS',
        payload: { tier, sectionId, status: 'approved' },
      })
    },
    [],
  )

  /** Set a section's status to 'rejected'. */
  const rejectSection = useCallback(
    (tier: string, sectionId: string) => {
      dispatch({
        type: 'UPDATE_SECTION_STATUS',
        payload: { tier, sectionId, status: 'rejected' },
      })
    },
    [],
  )

  /**
   * Set a section's status to 'edited' and store the edited content.
   * @param content — The user-modified content for this section.
   */
  const editSection = useCallback(
    (tier: string, sectionId: string, content: string) => {
      dispatch({
        type: 'UPDATE_SECTION_STATUS',
        payload: { tier, sectionId, status: 'edited', editedContent: content },
      })
    },
    [],
  )

  const value: WizardContextValue = {
    state,
    dispatch,
    currentStep: state.step,
    completedSteps: state.completedSteps,
    skippedSteps: state.skippedSteps,
    goNext,
    goBack,
    jumpTo,
    markComplete,
    skipStep,
    updateState,
    getMergeResult,
    setMergeResult,
    approveSection,
    rejectSection,
    editSection,
  }

  return (
    <WizardContext.Provider value={value}>
      {children}
    </WizardContext.Provider>
  )
}

/**
 * useWizard: Hook to access wizard context and navigation.
 * Throws if called outside WizardProvider.
 *
 * @returns WizardContextValue with all navigation and state methods
 *
 * @example
 * const { currentStep, goNext, goBack } = useWizard()
 * if (currentStep === WizardStep.Welcome) {
 *   return <button onClick={goNext}>Start</button>
 * }
 *
 * @throws Error if context is undefined (useWizard called outside WizardProvider)
 */
export function useWizard(): WizardContextValue {
  const context = useContext(WizardContext)
  if (context === undefined) {
    throw new Error(
      'useWizard must be called within a <WizardProvider>. ' +
      'Wrap your wizard routes with <WizardProvider> at a parent level.'
    )
  }
  return context
}
