/**
 * Purpose: Renders the correct step content panel for the current wizard step.
 * Inputs: currentStep from useWizard() hook.
 * Outputs: The matching step placeholder component (or WelcomePhase for step 1).
 * Constraints: Every WizardStep value must have a matching branch — exhaustive switch.
 *   Real content is out of scope for this ticket; placeholder panels used for all steps
 *   except Welcome (which already has WelcomePhase from phases/ directory).
 * SPORT: MASTER-COMPONENTS.md — WizardRouter
 */

import { WizardStep } from './types'
import { STEP_LABELS } from './stepLabels'
import { useWizard } from './WizardContext'
import { WelcomePhase } from './phases/WelcomePhase'
import { ArchiveLegacyPhase } from './phases/ArchiveLegacyPhase'
import { DaemonInstallPhase } from './phases/DaemonInstallPhase'
import { DonePhase } from './phases/DonePhase'
import { ScanLegacyPhase } from './phases/ScanLegacyPhase'
import { SymlinkSetupPhase } from './phases/SymlinkSetupPhase'
import { ToolModesPhase } from './phases/ToolModesPhase'

// ---------------------------------------------------------------------------
// Placeholder component for steps without real content yet
// ---------------------------------------------------------------------------

/**
 * Generic placeholder panel for wizard steps pending implementation.
 *
 * @param step - The WizardStep this placeholder represents.
 */
function StepPlaceholder({ step }: { step: WizardStep }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 px-8 py-12">
      <div className="flex h-16 w-16 items-center justify-center rounded-full bg-muted">
        <span className="text-2xl font-bold text-muted-foreground" aria-hidden="true">
          {step}
        </span>
      </div>
      <h2 className="text-xl font-semibold text-foreground">{STEP_LABELS[step]}</h2>
      <p className="max-w-sm text-center text-sm text-muted-foreground">
        This step is coming soon. Placeholder content for step {step}.
      </p>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

/**
 * Renders the content panel for the current wizard step.
 *
 * Switches on `currentStep` from WizardContext and renders the appropriate
 * phase component. Welcome renders the animated WelcomePhase; all other steps
 * render StepPlaceholder until their real content is implemented.
 *
 * @example
 * ```tsx
 * // Inside WizardLayout content panel:
 * <WizardRouter />
 * ```
 */
export function WizardRouter() {
  const { currentStep } = useWizard()

  switch (currentStep) {
    case WizardStep.Welcome:
      return <WelcomePhase />

    case WizardStep.ProviderConnect:
      return <StepPlaceholder step={WizardStep.ProviderConnect} />

    case WizardStep.ScanLegacy:
      return <ScanLegacyPhase />

    case WizardStep.MergeContent:
      return <StepPlaceholder step={WizardStep.MergeContent} />

    case WizardStep.ToolModes:
      return <ToolModesPhase />

    case WizardStep.VerifyDiff:
      return <StepPlaceholder step={WizardStep.VerifyDiff} />

    case WizardStep.ArchiveLegacy:
      return <ArchiveLegacyPhase />

    case WizardStep.SymlinkSetup:
      return <SymlinkSetupPhase />

    case WizardStep.DaemonInstall:
      return <DaemonInstallPhase />

    case WizardStep.Done:
      return <DonePhase />

    default: {
      // TypeScript exhaustiveness check
      const _exhaustive: never = currentStep
      return _exhaustive
    }
  }
}
