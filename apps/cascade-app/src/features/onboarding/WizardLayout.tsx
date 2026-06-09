/**
 * Purpose: Structural shell wrapping all 10 onboarding wizard phases.
 * Inputs:  None — reads navigation state from WizardContext via useWizard().
 * Outputs: Full-page layout with left sidebar (stepper), right content panel,
 *          and bottom navigation bar (Back / Next / Skip).
 * Constraints:
 *   - Renders WizardProvider internally so no parent setup is needed.
 *   - Keyboard: Left/Right arrow keys trigger Back/Next while focus is inside.
 *   - Responsive: minimum 800×600 (Tauri min window); works up to 1440×900+.
 *   - WCAG 2.1 AA: aria-current="step" on stepper, aria-labels on nav buttons,
 *     focus-visible rings on all interactive elements.
 *   - Left sidebar: fixed 240px. Content: flex-1. Bottom bar: sticky.
 * SPORT: MASTER-COMPONENTS.md — WizardLayout / WizardStepper
 */

import { useEffect, useRef } from 'react'
import { WizardProvider, useWizard } from './WizardContext'
import { WizardStepper } from './WizardStepper'
import { WizardRouter } from './WizardRouter'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { WizardStep } from './types'
import { STEP_ORDER } from './stepLabels'

// ---------------------------------------------------------------------------
// Inner layout (must be a child of WizardProvider to use useWizard)
// ---------------------------------------------------------------------------

/**
 * Inner layout shell. Must be mounted inside WizardProvider.
 * Handles keyboard events, nav wiring, and the three-panel layout.
 */
function WizardLayoutInner() {
  const { currentStep, completedSteps, goNext, goBack, jumpTo } = useWizard()
  const layoutRef = useRef<HTMLDivElement>(null)

  // Determine if this is the last step
  const isLastStep = currentStep === WizardStep.Done
  const currentIndex = STEP_ORDER.indexOf(currentStep)
  const canGoBack = currentIndex > 0
  const canGoNext = currentIndex < STEP_ORDER.length - 1

  // Keyboard handler: Left arrow = Back, Right arrow = Next
  // Attached as a global keydown listener scoped to the wizard container
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Only respond if focus is inside the wizard layout
      if (!layoutRef.current?.contains(document.activeElement)) return
      if (e.key === 'ArrowLeft' && canGoBack) {
        e.preventDefault()
        goBack()
      } else if (e.key === 'ArrowRight' && canGoNext) {
        e.preventDefault()
        goNext()
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [canGoBack, canGoNext, goBack, goNext])

  // Focus the layout container on mount so keyboard nav works immediately
  useEffect(() => {
    layoutRef.current?.focus()
  }, [])

  return (
    <div
      ref={layoutRef}
      className={cn(
        // Full-screen standalone layout, no app chrome
        'flex min-h-screen flex-col bg-background text-foreground',
        // Keyboard focus handling (tabIndex makes div focusable without triggering a11y errors)
        'outline-none',
      )}
      tabIndex={-1}
      aria-label="Onboarding wizard"
    >
      {/* Main area: sidebar + content */}
      <div className="flex flex-1 overflow-hidden">
        {/* Left sidebar — fixed 240px, full height, scrollable if needed */}
        <nav
          className={cn(
            'flex w-60 flex-shrink-0 flex-col border-r border-border bg-muted/30',
            'overflow-y-auto px-2 py-4',
          )}
          aria-label="Wizard step navigation"
        >
          {/* App title / logo area */}
          <div className="mb-6 px-3">
            <span className="text-lg font-bold tracking-tight text-foreground">Cascade</span>
            <p className="mt-0.5 text-xs text-muted-foreground">Setup Wizard</p>
          </div>

          {/* Step stepper */}
          <WizardStepper
            currentStep={currentStep}
            completedSteps={completedSteps}
            onStepClick={jumpTo}
          />
        </nav>

        {/* Right content panel — flex-1, scrollable */}
        <main
          className="flex flex-1 flex-col overflow-y-auto"
          id="wizard-content"
          aria-live="polite"
          aria-atomic="true"
        >
          <WizardRouter />
        </main>
      </div>

      {/* Bottom navigation bar — sticky */}
      <div
        className={cn(
          'flex items-center justify-between border-t border-border bg-background px-6 py-3',
          'sticky bottom-0',
        )}
        role="navigation"
        aria-label="Wizard navigation controls"
      >
        {/* Step progress indicator */}
        <span className="text-xs text-muted-foreground" aria-live="polite">
          Step {currentIndex + 1} of {STEP_ORDER.length}
        </span>

        {/* Nav buttons */}
        <div className="flex items-center gap-2">
          {/* Back button */}
          <Button
            variant="outline"
            size="sm"
            onClick={goBack}
            disabled={!canGoBack}
            aria-label="Go to previous step"
            aria-disabled={!canGoBack}
          >
            Back
          </Button>

          {/* Skip button — advance without marking complete */}
          {!isLastStep && (
            <Button
              variant="ghost"
              size="sm"
              onClick={goNext}
              aria-label="Skip this step"
            >
              Skip
            </Button>
          )}

          {/* Next / Finish button */}
          <Button
            variant="default"
            size="sm"
            onClick={goNext}
            disabled={!canGoNext}
            aria-label={isLastStep ? 'Finish setup' : 'Go to next step'}
            aria-disabled={!canGoNext}
          >
            {isLastStep ? 'Finish' : 'Next'}
          </Button>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Public export — wraps inner layout in WizardProvider
// ---------------------------------------------------------------------------

/**
 * Top-level wizard shell component.
 *
 * Renders WizardProvider, the sidebar stepper, content panel, and bottom
 * navigation bar. Mount this as a standalone route (not inside AppLayout).
 *
 * Keyboard navigation:
 * - Left arrow → Back
 * - Right arrow → Next
 *
 * Layout breakpoints:
 * - 800×600 (min Tauri window): sidebar + content stack correctly, bottom bar visible.
 * - 1440×900+: comfortable with generous content area.
 *
 * @example
 * ```tsx
 * // In routes/index.tsx:
 * <Route path="/onboarding" element={<WizardLayout />} />
 * ```
 */
export function WizardLayout() {
  return (
    <WizardProvider>
      <WizardLayoutInner />
    </WizardProvider>
  )
}
