/**
 * Purpose: TypeScript types for the Cascade onboarding merge engine.
 *   Defines the data contract for AI-driven legacy content merging.
 *
 * Inputs:  None — pure type definitions.
 * Outputs: Exported types consumed by mergeService, cascadeWriter, conflictDetector,
 *   and the Phase 4 merge UI.
 *
 * Constraints:
 *   - No any. Strict TypeScript.
 *   - All field names camelCase (Rust side uses serde rename_all = "camelCase").
 *   - MergeSection.id is a string UUID generated via crypto.randomUUID().
 *   - MergeResult.promptHash is sha256 of MergeRequest.userPrompt for re-run detection.
 *
 * SPORT: MASTER-COMPONENTS.md — MergeSection / MergeResult / MergeConflict types — merge/types.ts
 * Task: T-P3-E03-23
 */

import type { FileKind, ToolId } from '@/lib/scanner/types'
import type { WizardStep } from '@/features/onboarding/types'

// ---------------------------------------------------------------------------
// SectionStatus — the lifecycle state of one merged section
// ---------------------------------------------------------------------------

/**
 * The review state of a MergeSection.
 *   pending   — not yet reviewed by the user
 *   approved  — user confirmed this section for writing
 *   rejected  — user discarded this section
 *   edited    — user modified the proposedContent; editedContent holds the result
 */
export type SectionStatus = 'pending' | 'approved' | 'rejected' | 'edited'

// ---------------------------------------------------------------------------
// MergeSourceFile — one legacy file contributing to a merge
// ---------------------------------------------------------------------------

/**
 * A single legacy file that feeds into the AI merge for one tier/section.
 *
 * @param toolId   - Which AI tool this file came from.
 * @param path     - Absolute path to the original file.
 * @param content  - UTF-8 text content of the file (null if skipped: binary or >1 MB).
 * @param kind     - Semantic classification matching LegacyFile.kind.
 */
export interface MergeSourceFile {
  toolId: ToolId
  path: string
  content: string | null
  kind: FileKind
}

// ---------------------------------------------------------------------------
// MergeSection — one logical output section from the AI merge
// ---------------------------------------------------------------------------

/**
 * A single section produced by the AI merge pass for a given cascade tier.
 *
 * The user reviews each section in the DiffPanel and sets its status.
 * If status === 'edited', editedContent holds the user-modified version.
 * conflictWith references another section's id if the conflict detector
 * flagged an overlap (see MergeConflict).
 *
 * @param id               - UUID generated at merge time (crypto.randomUUID()).
 * @param title            - Human-readable section title (e.g. "Code Quality Rules").
 * @param sourceFiles      - Which legacy files this section was built from.
 * @param proposedContent  - AI-generated markdown content for this section.
 * @param status           - Current review status.
 * @param editedContent    - User-edited content; only set when status === 'edited'.
 * @param conflictWith     - ID of a conflicting sibling section, if any.
 */
export interface MergeSection {
  id: string
  title: string
  sourceFiles: MergeSourceFile[]
  proposedContent: string
  status: SectionStatus
  editedContent?: string
  conflictWith?: string
}

// ---------------------------------------------------------------------------
// MergeResult — full AI merge output for one wizard tier pass
// ---------------------------------------------------------------------------

/**
 * The complete result of one AI merge pass covering a single cascade tier.
 *
 * Stored in WizardState and used by the Phase 4 DiffPanel.
 *
 * @param tier         - Which wizard tier this merge covers (maps to a WizardStep).
 * @param sections     - All sections produced by the AI pass, pending user review.
 * @param generatedAt  - ISO-8601 timestamp when the AI call completed.
 * @param modelUsed    - Provider/model identifier (e.g. "gemini-flash-latest", "claude-opus-4").
 * @param promptHash   - SHA-256 hex of MergeRequest.userPrompt; used to detect re-run identity.
 */
export interface MergeResult {
  tier: WizardStep
  sections: MergeSection[]
  generatedAt: string
  modelUsed: string
  promptHash: string
}

// ---------------------------------------------------------------------------
// MergeConflict — advisory conflict between two sections
// ---------------------------------------------------------------------------

/**
 * Flags two MergeSection instances that may overlap or contradict.
 * Produced by conflictDetector.ts; advisory only — the user decides.
 *
 * @param sectionA  - First conflicting section.
 * @param sectionB  - Second conflicting section.
 * @param reason    - Human-readable description of why these sections conflict.
 */
export interface MergeConflict {
  sectionA: MergeSection
  sectionB: MergeSection
  reason: string
}

// ---------------------------------------------------------------------------
// MergeRequest — input payload sent to the AI merge service
// ---------------------------------------------------------------------------

/**
 * The payload passed to MergeService.mergeForTier().
 *
 * @param tier          - Which cascade tier this merge covers.
 * @param sourceFiles   - All legacy file contents to include in the prompt.
 * @param systemPrompt  - The versioned system prompt (see prompts/merge-v1.ts).
 * @param userPrompt    - The constructed user prompt with file contents inline.
 */
export interface MergeRequest {
  tier: WizardStep
  sourceFiles: MergeSourceFile[]
  systemPrompt: string
  userPrompt: string
}
