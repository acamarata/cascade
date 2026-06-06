/**
 * Purpose: Wizard state schema, read/write service, and validation.
 * Inputs: Filesystem path to wizard-state.json; partial state updates.
 * Outputs: Typed WizardState; persistence to .cascade/temp/wizard-state.json.
 * Constraints: Atomic write (write to .tmp then rename). Validates on load.
 *   All write paths respect dry_run flag — callers must check before writing.
 * SPORT: E5-S01-01 — wizard-state.json schema + read/write service
 */

import { invoke } from '@tauri-apps/api/core'
import { z } from 'zod'

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

export const ProviderConfigSchema = z.record(
  z.string(),
  z.object({
    provider_id: z.string(),
    connected: z.boolean(),
    account_hint: z.string().optional(), // masked email / username, never secret
    token_in_keychain: z.boolean().default(false),
  })
)

export const ToolDiscoverySchema = z.object({
  tool_id: z.string(), // e.g. "claude-code", "opencode", "cursor"
  home_dir: z.string(),
  file_count: z.number(),
  size_bytes: z.number(),
  last_modified: z.string().nullable(),
  detected: z.boolean(),
})

export const TierClassificationSchema = z.object({
  path: z.string(),
  tier: z.enum(['GCI', 'PCI', 'APC', 'PPC', 'PRC', 'PAC']),
  confidence: z.enum(['rule', 'heuristic', 'user_override']),
})

export const ProjectRootSchema = z.object({
  path: z.string(),
  name: z.string(),
  apc_parent: z.string().optional(),
  tier: z.enum(['PPC', 'PRC']),
  template_id: z.string().optional(),
  provider_override: z.string().optional(),
  tiers_merged: z.array(z.string()).default([]),
  archive_choices: z.record(z.string(), z.boolean()).default({}),
  symlinks_created: z.array(z.string()).default([]),
  status: z.enum(['pending', 'in_progress', 'complete', 'skipped']).default('pending'),
})

export const MergeSelectionSchema = z.record(
  z.string(), // section_id
  z.object({
    action: z.enum(['accept_ai', 'use_legacy', 'use_template', 'edit_manual', 'unresolved']),
    ai_confidence: z.number().optional(),
    user_edited: z.boolean().default(false),
  })
)

export const WizardStateSchema = z.object({
  version: z.literal(1).default(1),
  step_index: z.number().int().min(0).max(12).default(0),
  completed_steps: z.array(z.number()).default([]),
  skipped_steps: z.array(z.number()).default([]),
  dry_run: z.boolean().default(false),
  provider_config: ProviderConfigSchema.default({}),
  provider_skipped: z.boolean().default(false),
  tool_discoveries: z.record(z.string(), ToolDiscoverySchema).default({}),
  tier_classifications: z.record(z.string(), TierClassificationSchema).default({}),
  per_project_roots: z.array(ProjectRootSchema).default([]),
  current_project_index: z.number().int().default(0),
  merge_selections: MergeSelectionSchema.default({}),
  archive_choices: z.record(z.string(), z.boolean()).default({}),
  telemetry_opted_in: z.boolean().default(false),
  telemetry_endpoint: z.string().optional(),
  pci_root_path: z.string().optional(),
  apc_root_path: z.string().optional(),
  integration_modes: z
    .record(z.string(), z.enum(['cascade_managed', 'independent', 'custom']))
    .default({}),
  last_active_at: z.string().optional(),
  inactivity_timeout_mins: z.number().default(30),
})

export type WizardState = z.infer<typeof WizardStateSchema>
export type ProjectRoot = z.infer<typeof ProjectRootSchema>
export type TierClassification = z.infer<typeof TierClassificationSchema>

// ---------------------------------------------------------------------------
// Read / Write
// ---------------------------------------------------------------------------

/**
 * Purpose: Load wizard state from disk, validate, and return typed object.
 * Returns null if file does not exist (first run).
 * Throws if file exists but is corrupt and unrecoverable.
 */
export async function loadWizardState(): Promise<WizardState | null> {
  try {
    const raw: string | null = await invoke('wizard_state_read')
    if (raw === null) return null
    const parsed = JSON.parse(raw)
    const result = WizardStateSchema.safeParse(parsed)
    if (result.success) return result.data
    // Corrupt but partially valid — attempt recovery with defaults
    console.warn('wizard-state parse error:', result.error.message)
    return WizardStateSchema.parse({ ...parsed })
  } catch (err) {
    // File exists but is unreadable JSON — surface to caller
    throw new Error(`wizard-state.json corrupt: ${String(err)}`)
  }
}

/**
 * Purpose: Atomically write wizard state to .cascade/temp/wizard-state.json.
 * Constraints: Respects dry_run — in dry-run mode, logs the write but does not persist.
 *   Atomic write is handled by Tauri backend (write .tmp then rename).
 */
export async function saveWizardState(state: WizardState): Promise<void> {
  const updated: WizardState = {
    ...state,
    last_active_at: new Date().toISOString(),
  }
  if (state.dry_run) {
    // Dry-run: log the would-be write, do not persist
    await invoke('wizard_dry_run_log', {
      operation: 'WRITE',
      source: 'in-memory',
      destination: '.cascade/temp/wizard-state.json',
      payload: JSON.stringify(updated),
    })
    return
  }
  await invoke('wizard_state_write', { content: JSON.stringify(updated) })
}

/** Clears wizard state (--reset flag). Removes file from disk. */
export async function resetWizardState(): Promise<void> {
  await invoke('wizard_state_reset')
}

/** Marks a step complete and persists. */
export async function completeStep(state: WizardState, stepIndex: number): Promise<WizardState> {
  const next: WizardState = {
    ...state,
    step_index: stepIndex + 1,
    completed_steps: Array.from(new Set([...state.completed_steps, stepIndex])),
  }
  await saveWizardState(next)
  return next
}

/** Marks a step skipped and persists. */
export async function skipStep(state: WizardState, stepIndex: number): Promise<WizardState> {
  const next: WizardState = {
    ...state,
    step_index: stepIndex + 1,
    skipped_steps: Array.from(new Set([...state.skipped_steps, stepIndex])),
  }
  await saveWizardState(next)
  return next
}
