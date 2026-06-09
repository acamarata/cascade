/**
 * Purpose: Human-readable labels for each WizardStep value.
 * Inputs:  None — pure constant.
 * Outputs: STEP_LABELS record mapping WizardStep enum to display string.
 * Constraints: No React or side-effect imports. Values match the 10-step onboarding flow.
 *   Order must be 1..10 matching WizardStep enum numeric values.
 * SPORT: MASTER-COMPONENTS.md — stepLabels
 */

import { WizardStep } from './types'

/**
 * Display labels for each wizard step shown in the sidebar stepper.
 *
 * @example
 * ```ts
 * STEP_LABELS[WizardStep.Welcome] // "Welcome"
 * ```
 */
export const STEP_LABELS: Record<WizardStep, string> = {
  [WizardStep.Welcome]: 'Welcome',
  [WizardStep.ProviderConnect]: 'Connect AI',
  [WizardStep.ScanLegacy]: 'Discover Tools',
  [WizardStep.MergeContent]: 'Build Cascade',
  [WizardStep.ToolModes]: 'Configure Tools',
  [WizardStep.VerifyDiff]: 'Verify',
  [WizardStep.ArchiveLegacy]: 'Archive',
  [WizardStep.SymlinkSetup]: 'Setup Symlinks',
  [WizardStep.DaemonInstall]: 'Install Daemon',
  [WizardStep.Done]: 'Done',
}

/** Ordered list of all WizardStep values (1..10) for iteration. */
export const STEP_ORDER: WizardStep[] = [
  WizardStep.Welcome,
  WizardStep.ProviderConnect,
  WizardStep.ScanLegacy,
  WizardStep.MergeContent,
  WizardStep.ToolModes,
  WizardStep.VerifyDiff,
  WizardStep.ArchiveLegacy,
  WizardStep.SymlinkSetup,
  WizardStep.DaemonInstall,
  WizardStep.Done,
]
