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
import { MergeContentPhase } from './phases/MergeContentPhase'
import { ScanLegacyPhase } from './phases/ScanLegacyPhase'
import { SymlinkSetupPhase } from './phases/SymlinkSetupPhase'
import { ToolModesPhase } from './phases/ToolModesPhase'
import { VerifyDiffPhase } from './phases/VerifyDiffPhase'
// T-P3-E03-42: AI-optional gate — wraps AI-dependent steps
import { AIGatedStep } from '../../components/wizard/AIGatedStep'
// T-P3-E03-39b / T-P3-E03-40: Gemini Pool provisioning wizard step
import { GeminiPoolStep } from './steps/GeminiPoolStep'

// ---------------------------------------------------------------------------
// Placeholder component for steps without real content yet
// ---------------------------------------------------------------------------

/**
 * Generic placeholder panel for wizard steps pending implementation.
 * Retained for upcoming P3/P4 steps — not every step has a real component yet.
 *
 * @param step - The WizardStep this placeholder represents.
 */
/** Exported so future P3/P4 step files can use it without duplicating the scaffold. */
export function StepPlaceholder({ step }: { step: WizardStep }) {
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
  const { currentStep, jumpTo, skipStep } = useWizard()

  switch (currentStep) {
    case WizardStep.Welcome:
      return <WelcomePhase />

    case WizardStep.ProviderConnect:
      // T-P3-E03-39b / T-P3-E03-40: Provision Gemini API keys into pool
      return <GeminiPoolStep />

    case WizardStep.ScanLegacy:
      return <ScanLegacyPhase />

    case WizardStep.MergeContent:
      // T-P3-E03-42: Gate the AI-assisted merge step behind provider-connected check.
      // With no provider: show CTA card with "Connect a provider" + "Skip for now".
      // Skip records status='skipped' in wizard-state.json and advances to step 5.
      return (
        <AIGatedStep
          ctaText="AI merge requires a connected provider. Connect one to run the merge, or skip to configure tools manually."
          onConnectProvider={() => jumpTo(WizardStep.ProviderConnect)}
          onSkip={() => skipStep(WizardStep.MergeContent)}
        >
          <MergeContentPhase />
        </AIGatedStep>
      )

    case WizardStep.ToolModes:
      return <ToolModesPhase />

    case WizardStep.VerifyDiff:
      return <VerifyDiffPhase />

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
