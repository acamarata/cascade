/**
 * Purpose: Numbered step list for the onboarding wizard sidebar.
 * Inputs: currentStep, completedSteps, onStepClick callback from WizardLayout.
 * Outputs: <ol> with 11 step items; completed steps are clickable, future steps are not.
 * Constraints: WCAG 2.1 AA — aria-current="step" on active; aria-label on list.
 *   Completed steps have role="button" semantics (click to jump back).
 *   Future steps have no interactive affordance.
 * SPORT: MASTER-COMPONENTS.md — WizardStepper
 */

import { WizardStep } from './types'
import { STEP_LABELS, STEP_ORDER } from './stepLabels'
import { cn } from '@/lib/utils'

interface WizardStepperProps {
  /** Currently active step. */
  currentStep: WizardStep
  /** Set of already-completed steps (clickable for back-navigation). */
  completedSteps: Set<WizardStep>
  /** Callback when user clicks a completed step. */
  onStepClick: (step: WizardStep) => void
}

/**
 * Sidebar step list component.
 *
 * Renders all wizard steps as an ordered list. Completed steps display a
 * checkmark and are keyboard-accessible buttons. The active step receives
 * `aria-current="step"` per WCAG 2.1. Future steps are visually muted and
 * not interactive.
 *
 * @example
 * ```tsx
 * <WizardStepper
 *   currentStep={WizardStep.Welcome}
 *   completedSteps={new Set()}
 *   onStepClick={(step) => jumpTo(step)}
 * />
 * ```
 */
export function WizardStepper({ currentStep, completedSteps, onStepClick }: WizardStepperProps) {
  return (
    <ol className="flex flex-col gap-1 py-2" aria-label="Onboarding wizard steps">
      {STEP_ORDER.map((step, index) => {
        const isActive = step === currentStep
        const isCompleted = completedSteps.has(step)

        return (
          <li key={step}>
            {isCompleted ? (
              /* Completed step — clickable button for back-navigation */
              <button
                type="button"
                className={cn(
                  'flex w-full items-center gap-3 rounded-md px-3 py-2 text-left text-sm transition-colors',
                  'text-foreground hover:bg-accent hover:text-accent-foreground',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1'
                )}
                onClick={() => onStepClick(step)}
                aria-label={`Go back to step ${index + 1}: ${STEP_LABELS[step]}`}
              >
                <span
                  className="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground"
                  aria-hidden="true"
                >
                  {/* Checkmark SVG */}
                  <svg
                    viewBox="0 0 12 12"
                    fill="none"
                    xmlns="http://www.w3.org/2000/svg"
                    className="h-3 w-3"
                  >
                    <path
                      d="M2 6l3 3 5-5"
                      stroke="currentColor"
                      strokeWidth="1.5"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                    />
                  </svg>
                </span>
                <span className="truncate font-medium">{STEP_LABELS[step]}</span>
              </button>
            ) : isActive ? (
              /* Active step — non-interactive indicator */
              <div
                className={cn(
                  'flex items-center gap-3 rounded-md px-3 py-2',
                  'bg-accent text-accent-foreground'
                )}
                aria-current="step"
                role="listitem"
              >
                <span
                  className="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground"
                  aria-hidden="true"
                >
                  {index + 1}
                </span>
                <span className="truncate font-semibold">{STEP_LABELS[step]}</span>
              </div>
            ) : (
              /* Future step — visually muted, not interactive */
              <div
                className="flex items-center gap-3 rounded-md px-3 py-2 text-sm text-muted-foreground"
                aria-disabled="true"
              >
                <span
                  className="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full border border-muted-foreground/40 text-xs font-medium text-muted-foreground"
                  aria-hidden="true"
                >
                  {index + 1}
                </span>
                <span className="truncate">{STEP_LABELS[step]}</span>
              </div>
            )}
          </li>
        )
      })}
    </ol>
  )
}
