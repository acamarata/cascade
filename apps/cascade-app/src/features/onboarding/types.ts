/**
 * Purpose: TypeScript types for the Cascade onboarding wizard flow.
 * Inputs:  None — pure type definitions.
 * Outputs: WizardStep enum, WizardState interface, WizardCheckpoint type for JSON serialization.
 * Constraints: No React, Tauri, or external imports. Strict TS (no any). Enum values are numeric.
 *   WizardState.completedSteps is a Set, serialized as number[] in WizardCheckpoint.
 * SPORT: MASTER-COMPONENTS.md — WizardStep / WizardState / WizardCheckpoint types
 *   T-P3-E03-27: mergeResults (Partial<Record<string, MergeResult>>) added for section approval
 *   T-P3-E03-33: mergeResults included in WizardCheckpoint for persistence/resume
 */

import type { ToolId } from '@/lib/scanner/types'
import type { MergeResult as RichMergeResult, SectionStatus } from './merge/types'

// Re-export for consumers that need it without importing merge/types directly
export type { RichMergeResult as WizardMergeResult, SectionStatus }

/**
 * Tool mode selection: whether Cascade manages the tool's config via symlinks,
 * or the tool retains independent control of its own files.
 */
export type ToolMode = 'cascade-managed' | 'independent'

export type { ToolId }

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
 * - `detectedToolIds`: null until ScanLegacy completes; array of ToolIds found in the scan
 * - `mergeResult`: null until MergeContent completes; contains merge manifest and conflicts
 * - `mergeResults`: per-tier AI merge results keyed by tier id string ('global', 'project', etc.);
 *   populated incrementally as sections arrive (T-P3-E03-27). Each value holds the full
 *   RichMergeResult including sections and their approval status. Keyed by tier id string
 *   (not WizardStep number) because tiers have semantic names in the UI.
 * - `toolModes`: per-tool mode selection (cascade-managed | independent); populated by ToolModes step
 * - `archiveManifestPath`: null until ArchiveLegacy completes; path to the manifest file created
 * - `archivedTools`: map of toolId → archived flag; populated by Phase 7 after each success
 * - `symlinkPlanApplied`: true after SymlinkSetup step applies (or skips) the symlink plan
 * - `daemonInstalled`: true after daemon install step completes
 * - `startedAt`: ISO 8601 timestamp when wizard began
 * - `updatedAt`: ISO 8601 timestamp of last state change
 */
export interface WizardState {
  step: WizardStep
  completedSteps: Set<WizardStep>
  /**
   * Steps the user explicitly skipped (e.g. AI-gated steps with no provider connected).
   * A skipped step is still counted as traversed and will not block phase completion.
   * T-P3-E03-42: skipped steps appear in wizard-state.json as skipped status.
   */
  skippedSteps: Set<WizardStep>
  providerConnected: boolean
  scanResult: ScanResult | null
  /** ToolIds detected by the ScanLegacy step. Null until scan completes. */
  detectedToolIds: ToolId[] | null
  mergeResult: MergeResult | null
  /**
   * Per-tier AI merge results keyed by tier id string ('global', 'project', etc.).
   * Populated incrementally as each tier's AI merge completes. Each entry holds the full
   * RichMergeResult (sections, status, etc.) for that tier.
   * T-P3-E03-27: section approval actions (SET_MERGE_RESULT, UPDATE_SECTION_STATUS) write here.
   * T-P3-E03-33: persisted to checkpoint for resume-after-close.
   */
  mergeResults: Partial<Record<string, RichMergeResult>>
  /**
   * Per-tool mode selections from the ToolModes wizard step.
   * Keys are ToolId values; values are 'cascade-managed' | 'independent'.
   * Only populated tools are tracked; absent keys imply the default (cascade-managed).
   * Read by SymlinkSetupPhase (step 8) to determine which tools to symlink.
   */
  toolModes: Partial<Record<ToolId, ToolMode>>
  archiveManifestPath: string | null
  /**
   * Per-tool archive completion tracking.
   * Set to `true` for a toolId after `archive_legacy_tools` succeeds in Phase 7.
   * Read by Phase 8 (SymlinkSetup) to know which tools were archived this session.
   * Uses `Partial<>` so absent keys are equivalent to `false`.
   *
   * Task: T-P3-E03-21
   */
  archivedTools: Partial<Record<ToolId, boolean>>
  /** True after SymlinkSetup step applies (or skips) the symlink plan. */
  symlinkPlanApplied: boolean
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
  /**
   * Skipped steps serialized as number array for JSON compatibility.
   * T-P3-E03-42: AI-gated steps skipped by user (status='skipped').
   */
  skippedSteps: number[]
  providerConnected: boolean
  scanResult: ScanResult | null
  detectedToolIds: ToolId[] | null
  mergeResult: MergeResult | null
  /**
   * Per-tier AI merge results, serialized from WizardState.mergeResults.
   * RichMergeResult is JSON-safe (no functions, no circular refs).
   * T-P3-E03-33: persisted so phase 4 can resume after app close.
   * NOTE: If the Rust WizardCheckpoint struct is extended to persist mergeResults,
   * add a field: merge_results: Option<HashMap<String, serde_json::Value>> (camelCase via
   * serde rename_all = "camelCase"). Until then this field is written/read by the TS layer only.
   */
  mergeResults: Partial<Record<string, RichMergeResult>>
  toolModes: Partial<Record<ToolId, ToolMode>>
  archiveManifestPath: string | null
  /** Serialized form of WizardState.archivedTools (same shape — JSON-safe). */
  archivedTools: Partial<Record<ToolId, boolean>>
  symlinkPlanApplied: boolean
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
    skippedSteps: new Set<WizardStep>(),
    providerConnected: false,
    scanResult: null,
    detectedToolIds: null,
    mergeResult: null,
    mergeResults: {},
    toolModes: {},
    archiveManifestPath: null,
    archivedTools: {},
    symlinkPlanApplied: false,
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
    skippedSteps: Array.from(state.skippedSteps),
    providerConnected: state.providerConnected,
    scanResult: state.scanResult,
    detectedToolIds: state.detectedToolIds,
    mergeResult: state.mergeResult,
    mergeResults: state.mergeResults,
    toolModes: state.toolModes,
    archiveManifestPath: state.archiveManifestPath,
    archivedTools: state.archivedTools,
    symlinkPlanApplied: state.symlinkPlanApplied,
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
    skippedSteps: new Set(checkpoint.skippedSteps ?? []),
    providerConnected: checkpoint.providerConnected,
    scanResult: checkpoint.scanResult,
    detectedToolIds: checkpoint.detectedToolIds ?? null,
    mergeResult: checkpoint.mergeResult,
    mergeResults: checkpoint.mergeResults ?? {},
    toolModes: checkpoint.toolModes ?? {},
    archiveManifestPath: checkpoint.archiveManifestPath,
    archivedTools: checkpoint.archivedTools ?? {},
    symlinkPlanApplied: checkpoint.symlinkPlanApplied ?? false,
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
