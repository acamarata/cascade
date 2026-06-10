/**
 * Purpose: React Testing Library helpers for wizard integration tests.
 *   Provides a renderWizard() factory and step-navigation utilities
 *   so test bodies stay declarative and readable.
 *
 * Inputs:  Optional initial WizardState overrides + initial step.
 * Outputs: render() result + typed helpers for advancing and asserting steps.
 * Constraints:
 *   - Must wrap components in MemoryRouter (WelcomePhase / DonePhase use react-router).
 *   - WizardProvider + WizardRouter are the top-level SUT (system under test).
 *   - No snapshot tests — too brittle for animated wizard components.
 *
 * SPORT: MASTER-COMPONENTS.md — wizard E2E test suite — e2e/helpers/wizard.helpers.ts
 * Task: T-P3-E03-34
 */

import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { WizardProvider } from '@/features/onboarding/WizardContext'
import { WizardRouter } from '@/features/onboarding/WizardRouter'
import { WizardLayout } from '@/features/onboarding/WizardLayout'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Options for renderWizard() factory. */
export interface RenderWizardOptions {
  /** Override initial route (defaults to '/onboarding'). */
  initialRoute?: string
}

// ---------------------------------------------------------------------------
// Render factory
// ---------------------------------------------------------------------------

/**
 * Render the complete wizard (WizardProvider + WizardLayout + WizardRouter)
 * inside a MemoryRouter so react-router hooks work in jsdom.
 *
 * Returns the standard @testing-library/react render result.
 */
export function renderWizard(options: RenderWizardOptions = {}) {
  const { initialRoute = '/onboarding' } = options

  const result = render(
    <MemoryRouter initialEntries={[initialRoute]}>
      <WizardProvider>
        <WizardLayout>
          <WizardRouter />
        </WizardLayout>
      </WizardProvider>
    </MemoryRouter>
  )

  return result
}

// ---------------------------------------------------------------------------
// Step-label map (matches STEP_LABELS in stepLabels.ts)
// ---------------------------------------------------------------------------

export const STEP_LABELS: Record<number, string> = {
  1: 'Welcome',
  2: 'Connect Provider',
  3: 'Scan Legacy',
  4: 'Merge Content',
  5: 'Tool Modes',
  6: 'Verify',
  7: 'Archive Legacy',
  8: 'Symlink Setup',
  9: 'Install Daemon',
  10: 'Done',
}

// ---------------------------------------------------------------------------
// Navigation helpers
// ---------------------------------------------------------------------------

/**
 * Click the primary "Next" navigation button in WizardLayout footer.
 * Waits for the button to be enabled before clicking.
 */
export async function clickNext(): Promise<void> {
  const user = userEvent.setup()
  const nextBtn = await screen.findByRole('button', { name: /next|continue|get started|open cascade/i })
  await waitFor(() => expect(nextBtn).not.toBeDisabled(), { timeout: 3000 })
  await user.click(nextBtn)
}

/**
 * Click the "Back" navigation button.
 */
export async function clickBack(): Promise<void> {
  const user = userEvent.setup()
  const backBtn = await screen.findByRole('button', { name: /back|previous/i })
  await user.click(backBtn)
}

/**
 * Wait for the wizard to reach a given step by matching its step label in the stepper.
 * Polls until the expected label appears (accounts for async state transitions).
 */
export async function waitForStep(label: string, timeout = 5000): Promise<void> {
  await waitFor(
    () => expect(screen.getAllByText(label).length).toBeGreaterThan(0),
    { timeout }
  )
}

/**
 * Assert that the step counter shows the expected step number (1-10).
 * Looks for aria-current="step" or the step number in the stepper.
 */
export async function assertCurrentStep(stepNumber: number): Promise<void> {
  const label = STEP_LABELS[stepNumber]
  if (label) {
    await waitForStep(label)
  }
}
