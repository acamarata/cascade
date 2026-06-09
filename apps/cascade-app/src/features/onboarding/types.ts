/**
 * Purpose: TypeScript types for the Cascade onboarding wizard flow.
 * Inputs:  None — pure type definitions.
 * Outputs: WizardStep enum, WizardState interface, WizardCheckpoint type for JSON serialization.
 * Constraints: No React, Tauri, or external imports. Strict TS (no any). Enum values are numeric.
 *   WizardState.completedSteps is a Set, serialized as number[] in WizardCheckpoint.
 * SPORT: MASTER-COMPONENTS.md — WizardStep / WizardState / WizardCheckpoint types
 */

/**
 * Wizard step identifiers representing each stage of the onboarding flow.
 * Values are numeric for tight serialization and direct indexing.
 * Used to track current step and completed steps throughout the wizard.
 *
 * @example
 * ```ts
 * const currentStep: WizardStep = WizardStep.Welcome
 * const completed = new Set<WizardStep>([WizardStep.Welcome, WizardStep.ProviderConnect])
 * ```
 */
export const enum WizardStep {
  Welcome = 1,
  ProviderConnect = 2,
  ScanLegacy = 3,
  MergeContent = 4,
  ToolModes = 5,
  VerifyDiff = 6,
  ArchiveLegacy = 7,
  SymlinkSetup = 8,
  DaemonInstall = 9,
  Done = 10,
}

/**
 * Runtime state of the wizard.
 *
 * Persisted to wizard-state.json on each step change. Tracks progress through all wizard phases,
 * results from scans and merges, and daemon installation status.
 *
 * Fields:
 * - `step`: current active step
 * - `completedSteps`: Set of steps already completed (used to skip forward on resume)
 * - `providerConnected`: true after user authenticates at least one AI provider
 * - `scanResult`: null until ScanLegacy completes; contains file counts from legacy scan
 * - `mergeResult`: null until MergeContent completes; contains merge manifest and conflicts
 * - `archiveManifestPath`: null until ArchiveLegacy completes; path to the manifest file created
 * - `daemonInstalled`: true after daemon install step completes
 * - `startedAt`: ISO 8601 timestamp when wizard began
 * - `updatedAt`: ISO 8601 timestamp of last state change
 */
export interface WizardState {
  step: WizardStep
  completedSteps: Set<WizardStep>
  providerConnected: boolean
  scanResult: ScanResult | null
  mergeResult: MergeResult | null
  archiveManifestPath: string | null
  daemonInstalled: boolean
  startedAt: string
  updatedAt: string
}

/**
 * Result of scanning the legacy Cascade installation (shell + dashboard).
 * Populated when ScanLegacy step completes.
 */
export interface ScanResult {
  legacyConfigPath: string
  legacyVaultPath: string
  fileCount: number
  totalSizeBytes: number
}

/**
 * Result of merging content from legacy to new (Rust + Tauri) structure.
 * Populated when MergeContent step completes.
 */
export interface MergeResult {
  manifestPath: string
  mergedCount: number
  conflictCount: number
  skippedCount: number
}

/**
 * Serializable checkpoint format for wizard-state.json.
 *
 * Mirrors WizardState but with completedSteps serialized as a number array
 * (JSON does not support Set types natively) and timestamps as ISO 8601 strings.
 * The app deserializes this back to a WizardState when loading from disk.
 */
export interface WizardCheckpoint {
  step: WizardStep
  completedSteps: number[]
  providerConnected: boolean
  scanResult: ScanResult | null
  mergeResult: MergeResult | null
  archiveManifestPath: string | null
  daemonInstalled: boolean
  startedAt: string
  updatedAt: string
}

/**
 * Factory for the default initial WizardState.
 * Used as the init function for useReducer — called once on mount.
 *
 * @returns A fresh WizardState with step=Welcome and empty completedSteps.
 */
export function createInitialState(): WizardState {
  const now = new Date().toISOString()
  return {
    step: WizardStep.Welcome,
    completedSteps: new Set<WizardStep>(),
    providerConnected: false,
    scanResult: null,
    mergeResult: null,
    archiveManifestPath: null,
    daemonInstalled: false,
    startedAt: now,
    updatedAt: now,
  }
}

/**
 * Convert WizardState to WizardCheckpoint for JSON serialization.
 *
 * Converts the in-memory Set<WizardStep> to a number array for JSON compatibility,
 * and preserves all other fields.
 *
 * @param state - The runtime WizardState to serialize
 * @returns WizardCheckpoint suitable for JSON serialization
 */
export function stateToCheckpoint(state: WizardState): WizardCheckpoint {
  return {
    step: state.step,
    completedSteps: Array.from(state.completedSteps),
    providerConnected: state.providerConnected,
    scanResult: state.scanResult,
    mergeResult: state.mergeResult,
    archiveManifestPath: state.archiveManifestPath,
    daemonInstalled: state.daemonInstalled,
    startedAt: state.startedAt,
    updatedAt: state.updatedAt,
  }
}

/**
 * Convert WizardCheckpoint back to WizardState after deserialization.
 *
 * Reconstructs the Set<WizardStep> from the serialized number array.
 *
 * @param checkpoint - The deserialized WizardCheckpoint from JSON
 * @returns WizardState with Set reconstructed
 */
export function checkpointToState(checkpoint: WizardCheckpoint): WizardState {
  return {
    step: checkpoint.step,
    completedSteps: new Set(checkpoint.completedSteps),
    providerConnected: checkpoint.providerConnected,
    scanResult: checkpoint.scanResult,
    mergeResult: checkpoint.mergeResult,
    archiveManifestPath: checkpoint.archiveManifestPath,
    daemonInstalled: checkpoint.daemonInstalled,
    startedAt: checkpoint.startedAt,
    updatedAt: checkpoint.updatedAt,
  }
}

/**
 * App-level wizard status for routing decisions on startup.
 * Discriminated union with variant-keyed properties for programmatic routing.
 */
export type WizardStatus =
  | { NeverRun: true }
  | { InProgress: true }
  | { Complete: true }

/** WizardStatus constructor constants — use instead of namespace. */
export const WIZARD_STATUS = {
  NeverRun: { NeverRun: true } as WizardStatus,
  InProgress: { InProgress: true } as WizardStatus,
  Complete: { Complete: true } as WizardStatus,
} as const

/**
 * Result of the audit log format check during the onboarding wizard.
 * Populated by the audit check step to determine if legacy audit logs
 * need format migration or rotation.
 *
 * Fields:
 * - `auditLogExists`: true if ~/.cascade/audit.log exists
 * - `formatMismatch`: true if all records have pre-gate format (self-hash mismatch)
 * - `isTampered`: true if some records show genuine tampering
 * - `violationCount`: number of chain violations or format issues detected
 * - `rotated`: true if user chose to rotate the audit log during wizard
 */
export interface AuditCheckResult {
  auditLogExists: boolean
  formatMismatch: boolean
  isTampered: boolean
  violationCount: number
  rotated: boolean
}
