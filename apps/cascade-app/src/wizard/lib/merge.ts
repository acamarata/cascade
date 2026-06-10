/**
 * Purpose: AI-assisted and rule-based merge engine for CASCADE.md content.
 * Inputs: Legacy content sections, cascade template sections, AI provider (optional).
 * Outputs: MergeResult per section — merged text, confidence score, conflict markers.
 * Constraints: Rule-based merge always runs. AI layer is optional, provider-agnostic.
 *   Low confidence (<0.7) auto-flags for manual review.
 *   All merge results are written to wizard-merge-audit.json for traceability.
 * SPORT: E5-S14 — AI-assisted merge engine
 */

import { invoke } from '@tauri-apps/api/core'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type CascadeTier = 'GCI' | 'PCI' | 'APC' | 'PPC' | 'PRC' | 'PAC'

export interface ContentSection {
  id: string
  tier: CascadeTier
  heading: string
  body: string
  source: 'legacy' | 'template'
}

export type MergeAction = 'accept_ai' | 'use_legacy' | 'use_template' | 'edit_manual' | 'unresolved'

export interface MergeResult {
  section_id: string
  tier: CascadeTier
  heading: string
  legacy_text: string
  template_text: string
  /** Proposed merged text. Empty string if unresolved. */
  merged_text: string
  action: MergeAction
  /**
   * 0.0–1.0. 0 for rule-based merge (no AI). <0.7 flags for manual review.
   * undefined when no AI attempted.
   */
  ai_confidence?: number
  needs_manual_review: boolean
  conflict_type?: 'section_collision' | 'duplicate_key' | 'structural_mismatch' | 'none'
}

export interface MergeAuditEntry {
  section_id: string
  action: MergeAction
  timestamp: string
  ai_confidence?: number
  user_override: boolean
}

// ---------------------------------------------------------------------------
// Rule-based merge (baseline — no AI required)
// ---------------------------------------------------------------------------

/**
 * Purpose: Merge legacy and template content using deterministic rules.
 * Conflicts that cannot be resolved produce action="unresolved" with MANUAL_REVIEW_REQUIRED marker.
 *
 * Why rule-based first: ensures merge always produces an output even with no AI provider.
 * The AI layer is an enhancement, not a requirement.
 */
export function ruleBasedMerge(legacy: ContentSection, template: ContentSection): MergeResult {
  const base: Omit<
    MergeResult,
    'merged_text' | 'action' | 'needs_manual_review' | 'conflict_type'
  > = {
    section_id: legacy.id,
    tier: legacy.tier,
    heading: legacy.heading,
    legacy_text: legacy.body,
    template_text: template.body,
  }

  // Identical content — trivial merge
  if (legacy.body.trim() === template.body.trim()) {
    return {
      ...base,
      merged_text: legacy.body,
      action: 'use_legacy',
      needs_manual_review: false,
      conflict_type: 'none',
    }
  }

  // Template is empty — use legacy
  if (!template.body.trim()) {
    return {
      ...base,
      merged_text: legacy.body,
      action: 'use_legacy',
      needs_manual_review: false,
      conflict_type: 'none',
    }
  }

  // Legacy is empty — use template
  if (!legacy.body.trim()) {
    return {
      ...base,
      merged_text: template.body,
      action: 'use_template',
      needs_manual_review: false,
      conflict_type: 'none',
    }
  }

  // Structural mismatch — flag for manual review
  const unresolvedMarker = [
    '<!-- CASCADE_MANUAL_REVIEW_REQUIRED',
    `section: ${legacy.id}`,
    '--- LEGACY ---',
    legacy.body,
    '--- TEMPLATE ---',
    template.body,
    'CASCADE_MANUAL_REVIEW_REQUIRED -->',
  ].join('\n')

  return {
    ...base,
    merged_text: unresolvedMarker,
    action: 'unresolved',
    needs_manual_review: true,
    conflict_type: 'section_collision',
  }
}

// ---------------------------------------------------------------------------
// AI-enhanced merge
// ---------------------------------------------------------------------------

/**
 * Purpose: Send conflicting sections to the connected AI provider for merge suggestion.
 * Falls back to rule-based result if provider is unreachable or confidence is too low.
 */
export async function aiEnhancedMerge(
  legacy: ContentSection,
  template: ContentSection,
  providerId: string
): Promise<MergeResult> {
  try {
    const result: { merged_text: string; confidence: number } = await invoke('ai_merge_section', {
      provider_id: providerId,
      tier: legacy.tier,
      section_id: legacy.id,
      heading: legacy.heading,
      legacy_text: legacy.body,
      template_text: template.body,
    })

    const needsReview = result.confidence < 0.7

    return {
      section_id: legacy.id,
      tier: legacy.tier,
      heading: legacy.heading,
      legacy_text: legacy.body,
      template_text: template.body,
      merged_text: result.merged_text,
      action: needsReview ? 'unresolved' : 'accept_ai',
      ai_confidence: result.confidence,
      needs_manual_review: needsReview,
      conflict_type: 'none',
    }
  } catch {
    // AI unreachable — fall back to rule-based
    return ruleBasedMerge(legacy, template)
  }
}

// ---------------------------------------------------------------------------
// Tier-by-tier merge orchestration
// ---------------------------------------------------------------------------

export interface TierMergeInput {
  tier: CascadeTier
  legacy_sections: ContentSection[]
  template_sections: ContentSection[]
}

export interface TierMergeOutput {
  tier: CascadeTier
  results: MergeResult[]
  unresolved_count: number
  resolved_count: number
}

/**
 * Purpose: Run merge for all sections within a single cascade tier.
 * Order: GCI first, then PCI, APC, PPC, PRC, PAC.
 */
export async function mergeTier(
  input: TierMergeInput,
  providerId: string | null,
  dryRun: boolean
): Promise<TierMergeOutput> {
  const results: MergeResult[] = []

  for (const legacySection of input.legacy_sections) {
    const templateSection = input.template_sections.find((t) => t.id === legacySection.id)

    let result: MergeResult
    if (!templateSection) {
      // No template counterpart — preserve legacy as-is
      result = {
        section_id: legacySection.id,
        tier: legacySection.tier,
        heading: legacySection.heading,
        legacy_text: legacySection.body,
        template_text: '',
        merged_text: legacySection.body,
        action: 'use_legacy',
        needs_manual_review: false,
        conflict_type: 'none',
      }
    } else if (providerId) {
      result = await aiEnhancedMerge(legacySection, templateSection, providerId)
    } else {
      result = ruleBasedMerge(legacySection, templateSection)
    }

    results.push(result)

    if (!dryRun) {
      await appendMergeAudit({
        section_id: result.section_id,
        action: result.action,
        timestamp: new Date().toISOString(),
        ai_confidence: result.ai_confidence,
        user_override: false,
      })
    }
  }

  const unresolved = results.filter((r) => r.needs_manual_review).length
  return {
    tier: input.tier,
    results,
    unresolved_count: unresolved,
    resolved_count: results.length - unresolved,
  }
}

// ---------------------------------------------------------------------------
// Audit log
// ---------------------------------------------------------------------------

/**
 * Purpose: Append a merge decision to the audit log.
 * Log persists after wizard completion for traceability.
 */
async function appendMergeAudit(entry: MergeAuditEntry): Promise<void> {
  await invoke('wizard_merge_audit_append', { entry: JSON.stringify(entry) })
}

/** Record a user override decision (accept/reject/edit) against an AI or rule suggestion. */
export async function recordUserDecision(
  sectionId: string,
  action: MergeAction,
  aiConfidence?: number
): Promise<void> {
  await appendMergeAudit({
    section_id: sectionId,
    action,
    timestamp: new Date().toISOString(),
    ai_confidence: aiConfidence,
    user_override: true,
  })
}

// ---------------------------------------------------------------------------
// Cascade markdown output
// ---------------------------------------------------------------------------

/**
 * Purpose: Assemble merge results into a final CASCADE.md string.
 * Unresolved sections retain the MANUAL_REVIEW_REQUIRED marker.
 */
export function assembleCascadeMd(results: MergeResult[]): string {
  return results
    .map((r) => {
      const heading = r.heading ? `## ${r.heading}\n\n` : ''
      return `${heading}${r.merged_text}`
    })
    .join('\n\n---\n\n')
}
