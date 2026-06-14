/**
 * Purpose: Instruction-tier resolver and search service (E-P9-02).
 *   Fetches resolved CASCADE.md content for each of the six tiers via
 *   cascade_resolve IPC (or a fixture in dev/test), provides full-text
 *   search across all tiers, and computes per-tier diffs.
 * Inputs:  resolveFn — injectable IPC call for testing. Defaults to invoke('cascade_resolve').
 * Outputs: Promise<TierContent[]>, searchInstructions(), computeTierDiff().
 * Constraints:
 *   - Never throws; returns error-marked TierContent on failure.
 *   - Works in Vitest (Node) and Tauri webview via resolveFn injection.
 *   - computeTierDiff is pure (no I/O). searchInstructions is pure after tiers are loaded.
 * SPORT: MASTER-LIBS.md — instructionTiers service (E-P9-02)
 */

import type {
  TierContent,
  TierDiff,
  TierDiffLine,
  InstructionSearchResult,
  InstructionSearchHit,
} from '../../types/instructions'
import { TIER_LABELS } from '../../types/instructions'

// ── IPC helper ────────────────────────────────────────────────────────────────

/** Default IPC resolver — calls cascade_resolve via Tauri invoke. */
async function defaultResolve(tier: string): Promise<string> {
  const { invoke } = await import('@tauri-apps/api/core')
  const result = await invoke<{ content: string; format: string; tier: string }>(
    'cascade_resolve',
    { tier: tier.toLowerCase(), format: 'markdown' }
  )
  return result.content
}

// ── Tier fetching ──────────────────────────────────────────────────────────────

/**
 * Purpose: Fetch resolved CASCADE.md content for all six tiers.
 * Inputs:  resolveFn — optional injectable resolver (defaults to Tauri invoke).
 * Outputs: Promise<TierContent[]> — one entry per tier in TIER_LABELS order.
 * Constraints: Never throws. Each tier is fetched independently; failures are
 *   recorded as TierContent.error (tier is still present in the returned array).
 */
export async function fetchAllTiers(
  resolveFn: (tier: string) => Promise<string> = defaultResolve
): Promise<TierContent[]> {
  const results = await Promise.allSettled(
    TIER_LABELS.map((label) => resolveFn(label))
  )

  return TIER_LABELS.map((label, i) => {
    const result = results[i]
    if (result.status === 'fulfilled') {
      return {
        tier: label,
        content: result.value,
        present: result.value.trim().length > 0,
        error: null,
      } satisfies TierContent
    }
    return {
      tier: label,
      content: '',
      present: false,
      error: result.reason instanceof Error ? result.reason.message : String(result.reason),
    } satisfies TierContent
  })
}

// ── Full-text search ──────────────────────────────────────────────────────────

/**
 * Purpose: Search for a query string across all loaded tier contents.
 * Inputs:  tiers — already-fetched tier contents; query — search string (case-insensitive).
 *          maxHits — cap on returned hits (default 200).
 * Outputs: InstructionSearchResult with all matching lines.
 * Constraints: Pure function (no I/O). Case-insensitive match.
 *   Empty query returns { hits: [], totalHits: 0 }.
 */
export function searchInstructions(
  tiers: TierContent[],
  query: string,
  maxHits = 200
): InstructionSearchResult {
  if (!query.trim()) return { hits: [], totalHits: 0 }

  const lowerQuery = query.toLowerCase()
  const hits: InstructionSearchHit[] = []
  let totalHits = 0

  for (const tier of tiers) {
    if (!tier.present || !tier.content) continue

    const lines = tier.content.split('\n')
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i]
      const lowerLine = line.toLowerCase()
      const idx = lowerLine.indexOf(lowerQuery)
      if (idx === -1) continue

      totalHits++
      if (hits.length < maxHits) {
        hits.push({
          tier: tier.tier,
          lineNo: i + 1,
          lineText: line,
          matchStart: idx,
          matchEnd: idx + query.length,
        } satisfies InstructionSearchHit)
      }
    }
  }

  return { hits, totalHits }
}

// ── Tier diff ─────────────────────────────────────────────────────────────────

/**
 * Purpose: Compute what a tier adds vs all prior tiers combined.
 * Inputs:  tiers — full ordered tier list; tierIndex — index of the tier to diff.
 * Outputs: TierDiff with per-line isNew annotations.
 * Constraints: Pure function (no I/O). "New" = line not present in ANY prior tier's content.
 *   Comparison is trim()-normalized. Empty tiers produce zero diffs.
 */
export function computeTierDiff(tiers: TierContent[], tierIndex: number): TierDiff {
  const tier = tiers[tierIndex]
  if (!tier) {
    return { tier: 'GCI', lines: [], newLineCount: 0, inheritedLineCount: 0 }
  }

  // Build set of all lines from prior tiers (trim-normalised).
  const priorLines = new Set<string>()
  for (let i = 0; i < tierIndex; i++) {
    const prior = tiers[i]
    if (!prior.present) continue
    for (const line of prior.content.split('\n')) {
      priorLines.add(line.trim())
    }
  }

  const lines: TierDiffLine[] = []
  let newLineCount = 0
  let inheritedLineCount = 0

  if (tier.present) {
    for (const text of tier.content.split('\n')) {
      const isNew = !priorLines.has(text.trim())
      lines.push({ text, isNew })
      if (isNew) newLineCount++
      else inheritedLineCount++
    }
  }

  return { tier: tier.tier, lines, newLineCount, inheritedLineCount }
}
