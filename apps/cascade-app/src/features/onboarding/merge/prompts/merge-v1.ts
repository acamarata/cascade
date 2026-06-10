/**
 * Purpose: Versioned merge prompt template (v1) for the Cascade AI merge engine.
 *   Contains the exact system prompt text sent to the AI provider, plus helpers for
 *   building the user prompt from source files and computing the prompt hash.
 *
 * Inputs:  tier (string), source files array.
 * Outputs: { systemPrompt, userPrompt, promptHash } consumed by mergeService.ts.
 *
 * Constraints:
 *   - NEVER edit the systemPrompt text without bumping PROMPT_VERSION.
 *   - promptHash is SHA-256 hex of the userPrompt string; stored in MergeResult for
 *     re-run identity detection.
 *   - No Tauri or React imports — pure logic module, testable in isolation.
 *
 * SPORT: MASTER-COMPONENTS.md — merge-v1 prompt — merge/prompts/merge-v1.ts
 * Task: T-P3-E03-25
 */

import type { MergeSourceFile } from '../types'

// ---------------------------------------------------------------------------
// Version
// ---------------------------------------------------------------------------

/** Increment when the system prompt text changes. */
export const PROMPT_VERSION = 'v1' as const

// ---------------------------------------------------------------------------
// System prompt (verbatim — matches spec T-P3-E03-25)
// ---------------------------------------------------------------------------

/**
 * System prompt sent to the AI provider for all merge requests.
 *
 * Rules embedded in the prompt:
 *   1. Preserve all user-authored rules and preferences; never discard.
 *   2. Deduplicate: if multiple tools say the same thing, say it once.
 *   3. Normalize: convert tool-specific syntax to cascade markdown format.
 *   4. Preserve memory, tasks, ideas as-is under their respective sections.
 *   5. Output format: JSON with structure {sections: [{id, title, content, sourceFiles}]}
 */
export const SYSTEM_PROMPT_V1 =
  'You are a cascade content builder. You receive legacy AI instruction files from multiple ' +
  'tools and produce a unified CASCADE.md section for the {tier} tier. Rules:\n' +
  '(1) Preserve all user-authored rules and preferences; never discard.\n' +
  '(2) Deduplicate: if multiple tools say the same thing, say it once.\n' +
  '(3) Normalize: convert tool-specific syntax (CLAUDE.md headers, .cursorrules format) to ' +
  'cascade markdown format.\n' +
  '(4) Preserve memory, tasks, ideas as-is under their respective sections.\n' +
  '(5) Output format: JSON with structure {sections: [{id, title, content, sourceFiles}]}'

// ---------------------------------------------------------------------------
// Prompt hash (SHA-256 via Web Crypto API)
// ---------------------------------------------------------------------------

/**
 * Compute SHA-256 hex of the given string using the Web Crypto API.
 *
 * Returns a 64-character lowercase hex string.
 * Non-blocking: uses SubtleCrypto which is available in both browser and Tauri contexts.
 *
 * @param input - The string to hash (typically the user prompt).
 */
export async function sha256Hex(input: string): Promise<string> {
  const encoder = new TextEncoder()
  const data = encoder.encode(input)
  const hashBuffer = await crypto.subtle.digest('SHA-256', data)
  const hashArray = Array.from(new Uint8Array(hashBuffer))
  return hashArray.map((b) => b.toString(16).padStart(2, '0')).join('')
}

// ---------------------------------------------------------------------------
// System prompt builder — substitutes {tier} placeholder
// ---------------------------------------------------------------------------

/**
 * Build the system prompt for the given tier by substituting the {tier} placeholder.
 *
 * @param tier - Human-readable tier name (e.g. "global", "project").
 */
export function buildSystemPrompt(tier: string): string {
  return SYSTEM_PROMPT_V1.replace('{tier}', tier)
}

// ---------------------------------------------------------------------------
// User prompt builder — assembles file contents into one prompt
// ---------------------------------------------------------------------------

/**
 * Build the user prompt by inlining all source file contents.
 *
 * Files with null content (skipped/binary) are included as a header-only entry
 * so the model knows they existed but were unreadable.
 *
 * @param tier        - Human-readable tier name.
 * @param sourceFiles - Legacy files to merge.
 */
export function buildUserPrompt(tier: string, sourceFiles: MergeSourceFile[]): string {
  const fileBlocks = sourceFiles
    .map((f) => {
      const header = `## File: ${f.path}\nTool: ${f.toolId}\nKind: ${f.kind}`
      if (f.content === null) {
        return `${header}\n[SKIPPED — binary or oversized file]\n`
      }
      return `${header}\n\n${f.content}\n`
    })
    .join('\n---\n\n')

  return (
    `Merge the following legacy AI instruction files for the "${tier}" tier.\n\n` +
    `Source files (${sourceFiles.length}):\n\n` +
    fileBlocks +
    `\n\nProduce the unified CASCADE.md sections as JSON per the system prompt rules.`
  )
}

// ---------------------------------------------------------------------------
// Convenience: build all prompt parts and hash in one call
// ---------------------------------------------------------------------------

/**
 * Build all prompt parts required by MergeService.mergeForTier():
 * system prompt, user prompt, and SHA-256 hash of the user prompt.
 *
 * @param tier        - Human-readable tier name.
 * @param sourceFiles - Legacy files to include in the prompt.
 */
export async function buildMergePrompts(
  tier: string,
  sourceFiles: MergeSourceFile[],
): Promise<{ systemPrompt: string; userPrompt: string; promptHash: string }> {
  const systemPrompt = buildSystemPrompt(tier)
  const userPrompt = buildUserPrompt(tier, sourceFiles)
  const promptHash = await sha256Hex(userPrompt)
  return { systemPrompt, userPrompt, promptHash }
}
