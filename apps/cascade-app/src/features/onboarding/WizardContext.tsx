/**
 * Purpose: React context for wizard state management and navigation.
 * Inputs: WizardProvider wraps children; useWizard() hook consumed by wizard UI.
 * Outputs: Typed hook exposing current state, navigation, and completion tracking.
 * Constraints: useWizard must be called within WizardProvider (throws if outside).
 *   State changes are synchronous; checkpoint persistence is caller's responsibility.
 * SPORT: E5-S01-02 — WizardContext + useWizard hook
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

/**
 * WizardContextValue: The shape of data exposed by the context.
 */
interface WizardContextValue {
  state: WizardState
  dispatch: (action: WizardAction) => void
  currentStep: WizardStep
  completedSteps: Set<WizardStep>
  goNext: () => void
  goBack: () => void
  jumpTo: (step: WizardStep) => void
  markComplete: (step: WizardStep) => void
  updateState: (partial: Partial<WizardState>) => void
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

  const updateState = useCallback((partial: Partial<WizardState>) => {
    dispatch({ type: 'UPDATE_STATE', payload: partial })
  }, [])

  const value: WizardContextValue = {
    state,
    dispatch,
    currentStep: state.step,
    completedSteps: state.completedSteps,
    goNext,
    goBack,
    jumpTo,
    markComplete,
    updateState,
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
