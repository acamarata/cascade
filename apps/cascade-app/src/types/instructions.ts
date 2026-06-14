/**
 * Purpose: TypeScript types for the instruction-tier knowledge object (E-P9-02).
 *   Mirrors the six-tier CASCADE.md resolution model.
 * Inputs:  None — pure type definitions.
 * Outputs: InstructionTier, TierContent, TierDiff, InstructionSearchHit.
 * Constraints: No React/Tauri imports. Strict TS (no any).
 * SPORT: MASTER-LIBS.md — instructions types (E-P9-02)
 */

/** The six instruction tiers in resolution order (GCI wins at top). */
export type TierLabel = 'GCI' | 'PCI' | 'APC' | 'PPC' | 'PRC' | 'PAC'

/** Ordered list of all six tier labels. */
export const TIER_LABELS: readonly TierLabel[] = ['GCI', 'PCI', 'APC', 'PPC', 'PRC', 'PAC'] as const

/** Human-readable description for each tier. */
export const TIER_DESCRIPTIONS: Record<TierLabel, string> = {
  GCI: 'Global Claude Instructions — applies to all projects',
  PCI: 'Personal Claude Instructions — personal/downloads context',
  APC: 'All-Projects Claude Instructions — all coding projects under ~/Sites',
  PPC: 'Per-Project Claude Instructions — one multi-repo project',
  PRC: 'Per-Repo Claude Instructions — one repo',
  PAC: 'Per-App Claude Instructions — one app in a multi-app repo',
}

/**
 * Content of a single instruction tier resolved via cascade_resolve.
 */
export interface TierContent {
  /** Tier label, e.g. "GCI". */
  tier: TierLabel
  /** Resolved CASCADE.md content for this tier (empty string when absent). */
  content: string
  /** True when the tier file exists and was resolved successfully. */
  present: boolean
  /** Error message if resolution failed, null otherwise. */
  error: string | null
}

/**
 * A line-level diff entry showing what one tier adds vs the previous.
 * Used by the tier inheritance diff view.
 */
export interface TierDiffLine {
  /** Line text. */
  text: string
  /** Whether this line only appears in the current tier (not in prior tiers). */
  isNew: boolean
}

/**
 * Diff summary: what `tier` adds on top of all prior tiers combined.
 */
export interface TierDiff {
  /** The tier being diffed. */
  tier: TierLabel
  /** Lines present in this tier (annotated with isNew). */
  lines: TierDiffLine[]
  /** Count of lines new in this tier. */
  newLineCount: number
  /** Count of lines inherited from prior tiers. */
  inheritedLineCount: number
}

/**
 * A single full-text search hit across all instruction tiers.
 */
export interface InstructionSearchHit {
  /** Tier where the match was found. */
  tier: TierLabel
  /** 1-based line number within the tier content. */
  lineNo: number
  /** The matching line text. */
  lineText: string
  /** Byte offset of the match within lineText (for highlight). */
  matchStart: number
  /** End offset of the match within lineText (exclusive). */
  matchEnd: number
}

/**
 * Result of a full-text search across all instruction tiers.
 */
export interface InstructionSearchResult {
  /** All hits across all tiers, ordered by tier then line. */
  hits: InstructionSearchHit[]
  /** Total hit count (may be capped by the caller). */
  totalHits: number
}
