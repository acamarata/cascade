/**
 * Purpose: AI merge call service for the Cascade onboarding wizard.
 *   Orchestrates provider selection, prompt construction, Tauri command invocation,
 *   and result normalization for one wizard tier pass.
 *
 * Inputs:  tier (WizardStep), sourceFiles (MergeSourceFile[]), wizard state for routing.
 * Outputs: MergeResult — always resolves (parse errors return a single raw-output section).
 *
 * Provider routing:
 *   getActiveProvider() resolves the first available provider (GeminiPool > Anthropic >
 *   OpenAI > Local). If Gemini Pool is detected, the Tauri run_ai_merge command is used
 *   (which calls localhost:3761 on the Rust side). For all other providers the TS-side
 *   fetch is used directly via the Provider.call() interface.
 *
 * Retry: 3 attempts, 2 s backoff on network error or provider rate limit.
 *
 * Constraints:
 *   - Never throws: parse failures return a MergeResult with parseError section.
 *   - No credentials stored here; they flow through ProviderRoutingContext / E-04.
 *   - run_ai_merge Tauri command used for GeminiPool (proxy rotates keys on Rust side).
 *
 * SPORT: MASTER-COMPONENTS.md — mergeService — merge/mergeService.ts
 * Task: T-P3-E03-25
 */

import { invoke } from '@tauri-apps/api/core'
import type { MergeRequest, MergeResult, MergeSection, MergeSourceFile } from './types'
import type { ProviderRoutingContext } from './providerRouter'
import { getActiveProvider } from './providerRouter'
import { buildMergePrompts, PROMPT_VERSION } from './prompts/merge-v1'
import type { WizardStep } from '@/features/onboarding/types'

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const MAX_RETRIES = 3
const RETRY_DELAY_MS = 2_000

// ---------------------------------------------------------------------------
// Tier label mapping — WizardStep → human-readable tier name for prompts
// ---------------------------------------------------------------------------

/**
 * Map a WizardStep to a human-readable tier name used in the merge prompt.
 * Defaults to the numeric value if the step has no explicit mapping.
 */
function tierLabel(tier: WizardStep): string {
  const labels: Partial<Record<number, string>> = {
    4: 'global',   // MergeContent = 4
    5: 'project',  // ToolModes = 5
  }
  return labels[tier as number] ?? `tier-${tier}`
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Run the AI merge for a single wizard tier and return a structured MergeResult.
 *
 * Strategy:
 *   1. Resolve active provider via getActiveProvider().
 *   2. Build system + user prompts from merge-v1 template.
 *   3. If GeminiPool detected → invoke run_ai_merge Tauri command (Rust calls proxy).
 *      Otherwise → call provider directly via Provider.call().
 *   4. Parse the model text response into MergeResult.sections.
 *   5. On parse failure → single section with id "parse-error" and parseError flag.
 *   6. Retry up to MAX_RETRIES on network errors or rate limits.
 *
 * @param tier        - WizardStep this merge covers (e.g. WizardStep.MergeContent = 4).
 * @param sourceFiles - Legacy files prepared by the reader step.
 * @param ctx         - Minimal wizard state for provider routing.
 */
export async function mergeForTier(
  tier: WizardStep,
  sourceFiles: MergeSourceFile[],
  ctx: ProviderRoutingContext,
): Promise<MergeResult> {
  const label = tierLabel(tier)
  const { systemPrompt, userPrompt, promptHash } = await buildMergePrompts(label, sourceFiles)

  const provider = await getActiveProvider(ctx)

  if (!provider) {
    return makeErrorResult(
      tier,
      sourceFiles,
      promptHash,
      'No AI provider available. Connect a provider in the wizard.',
    )
  }

  // If provider is GeminiPool, delegate entirely to the Tauri run_ai_merge command.
  // The Rust side calls localhost:3761 directly and handles retry + parsing.
  if (provider.name === 'gemini-2.0-flash') {
    return runViaTauri(tier, sourceFiles, systemPrompt, userPrompt, promptHash)
  }

  // For non-Gemini providers: call directly from TS with retry.
  return runWithRetry(tier, sourceFiles, provider, { tier, sourceFiles, systemPrompt, userPrompt }, promptHash)
}

// ---------------------------------------------------------------------------
// Tauri path (GeminiPool)
// ---------------------------------------------------------------------------

/**
 * Invoke the run_ai_merge Tauri command (Rust calls localhost:3761).
 * Returns the MergeResult directly from Rust — parsing happens on the Rust side.
 */
async function runViaTauri(
  tier: WizardStep,
  sourceFiles: MergeSourceFile[],
  systemPrompt: string,
  userPrompt: string,
  promptHash: string,
): Promise<MergeResult> {
  try {
    const result = await invoke<MergeResult>('run_ai_merge', {
      tier: tier as number,
      sourceFiles,
      systemPrompt,
      userPrompt,
      promptHash,
    })
    return result
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err)
    return makeErrorResult(tier, sourceFiles, promptHash, `run_ai_merge command failed: ${msg}`)
  }
}

// ---------------------------------------------------------------------------
// Direct provider path (Anthropic / OpenAI / Local) with retry
// ---------------------------------------------------------------------------

async function runWithRetry(
  tier: WizardStep,
  sourceFiles: MergeSourceFile[],
  provider: Awaited<ReturnType<typeof getActiveProvider>> & {},
  request: MergeRequest,
  promptHash: string,
): Promise<MergeResult> {
  let lastError = ''

  for (let attempt = 0; attempt < MAX_RETRIES; attempt++) {
    if (attempt > 0) {
      await delay(RETRY_DELAY_MS)
    }

    try {
      const modelText = await provider.call(request)
      return parseModelText(tier, sourceFiles, modelText, provider.name, promptHash)
    } catch (err) {
      lastError = err instanceof Error ? err.message : String(err)
      // Retry on network / rate-limit errors.
      const isRetryable =
        lastError.includes('429') ||
        lastError.includes('rate limit') ||
        lastError.includes('network') ||
        lastError.includes('fetch') ||
        lastError.includes('timeout')
      if (!isRetryable) break
    }
  }

  return makeErrorResult(
    tier,
    sourceFiles,
    promptHash,
    `Provider call failed after ${MAX_RETRIES} attempts: ${lastError}`,
  )
}

// ---------------------------------------------------------------------------
// Response parser
// ---------------------------------------------------------------------------

/** Shape the AI model is asked to return. */
interface AiModelOutput {
  sections?: Array<{
    id?: string
    title?: string
    content?: string
    sourceFiles?: string[]
  }>
}

/**
 * Parse the raw model text into a MergeResult.
 *
 * Attempts:
 *   1. Direct JSON.parse
 *   2. Strip leading ```json / trailing ``` fence, then JSON.parse
 *   3. Fall back to a single parse-error section with the raw text.
 */
function parseModelText(
  tier: WizardStep,
  sourceFiles: MergeSourceFile[],
  modelText: string,
  modelUsed: string,
  promptHash: string,
): MergeResult {
  const generatedAt = new Date().toISOString()

  // Attempt 1: direct parse.
  let parsed: AiModelOutput | null = tryParse(modelText)

  // Attempt 2: strip fence.
  if (!parsed) {
    parsed = tryParse(stripJsonFence(modelText))
  }

  if (parsed?.sections && parsed.sections.length > 0) {
    const sections: MergeSection[] = parsed.sections
      .filter((s) => s.title && s.title.trim().length > 0)
      .map((s, i) => ({
        id: s.id?.trim() || `section-${i}`,
        title: s.title!.trim(),
        sourceFiles,
        proposedContent: s.content ?? '',
        status: 'pending' as const,
        editedContent: undefined,
        conflictWith: undefined,
      }))

    if (sections.length > 0) {
      return { tier, sections, generatedAt, modelUsed, promptHash }
    }
  }

  // Parse failure path.
  return makeParseErrorResult(tier, modelText, sourceFiles, modelUsed, promptHash, generatedAt)
}

function tryParse(text: string): AiModelOutput | null {
  try {
    return JSON.parse(text) as AiModelOutput
  } catch {
    return null
  }
}

function stripJsonFence(text: string): string {
  let t = text.trim()
  if (t.startsWith('```json')) t = t.slice(7)
  else if (t.startsWith('```')) t = t.slice(3)
  if (t.endsWith('```')) t = t.slice(0, -3)
  return t.trim()
}

// ---------------------------------------------------------------------------
// Error result builders
// ---------------------------------------------------------------------------

function makeParseErrorResult(
  tier: WizardStep,
  rawText: string,
  sourceFiles: MergeSourceFile[],
  modelUsed: string,
  promptHash: string,
  generatedAt: string,
): MergeResult {
  return {
    tier,
    sections: [
      {
        id: 'parse-error',
        title: 'Raw AI Output (parse failed)',
        sourceFiles,
        proposedContent: rawText,
        status: 'pending',
      },
    ],
    generatedAt,
    modelUsed,
    promptHash,
  }
}

function makeErrorResult(
  tier: WizardStep,
  sourceFiles: MergeSourceFile[],
  promptHash: string,
  message: string,
): MergeResult {
  return {
    tier,
    sections: [
      {
        id: 'error',
        title: 'Error',
        sourceFiles,
        proposedContent: message,
        status: 'pending',
      },
    ],
    generatedAt: new Date().toISOString(),
    modelUsed: 'none',
    promptHash,
  }
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

// ---------------------------------------------------------------------------
// mergeSection — single-section re-run with custom instruction (T-P3-E03-31)
// ---------------------------------------------------------------------------

/**
 * Re-run the AI merge for a single section with a custom user instruction.
 *
 * Strategy:
 *   Uses the same provider routing as mergeForTier. The custom instruction is
 *   appended to the system prompt so the model applies it only to this section.
 *   The returned MergeSection preserves the original id and resets status to 'pending'
 *   so the user must re-approve it.
 *
 * @param sectionId         - The id of the section to re-run (preserved in the result).
 * @param customInstruction - User-provided instruction appended to the system prompt.
 * @param sourceFiles       - Legacy files to re-merge (same set used in the original merge).
 * @param ctx               - Minimal wizard state for provider routing.
 */
export async function mergeSection(
  sectionId: string,
  customInstruction: string,
  sourceFiles: MergeSourceFile[],
  ctx: ProviderRoutingContext,
): Promise<MergeSection> {
  const label = 'global' // Section re-runs always use the global tier prompt context
  const { systemPrompt, userPrompt, promptHash } = await buildMergePrompts(label, sourceFiles)

  // Append the custom instruction to the system prompt (per spec guide)
  const customSystemPrompt =
    systemPrompt + `\n\nFor this specific section only: ${customInstruction}`

  const provider = await getActiveProvider(ctx)

  if (!provider) {
    return makeSectionErrorResult(sectionId, 'No AI provider available. Connect a provider in the wizard.')
  }

  const request: MergeRequest = {
    tier: 4 as WizardStep, // MergeContent = 4
    sourceFiles,
    systemPrompt: customSystemPrompt,
    userPrompt,
  }

  let lastError = ''
  for (let attempt = 0; attempt < MAX_RETRIES; attempt++) {
    if (attempt > 0) await delay(RETRY_DELAY_MS)
    try {
      const modelText = await provider.call(request)
      // Parse the result and return the first valid section (or a parse-error section)
      const fullResult = parseModelText(4 as WizardStep, sourceFiles, modelText, provider.name, promptHash)
      const firstSection = fullResult.sections[0]
      if (!firstSection) {
        return makeSectionErrorResult(sectionId, 'AI returned no sections.')
      }
      // Preserve the original section id; reset status to pending for re-approval
      return {
        ...firstSection,
        id: sectionId,
        status: 'pending',
        editedContent: undefined,
      }
    } catch (err) {
      lastError = err instanceof Error ? err.message : String(err)
      const isRetryable =
        lastError.includes('429') ||
        lastError.includes('rate limit') ||
        lastError.includes('network') ||
        lastError.includes('fetch') ||
        lastError.includes('timeout')
      if (!isRetryable) break
    }
  }

  return makeSectionErrorResult(sectionId, `Re-run failed after ${MAX_RETRIES} attempts: ${lastError}`)
}

/**
 * Build a single error MergeSection for the re-run error path.
 *
 * @param sectionId - The original section id to preserve.
 * @param message   - Error description surfaced in proposedContent.
 */
function makeSectionErrorResult(sectionId: string, message: string): MergeSection {
  return {
    id: sectionId,
    title: 'Re-run Error',
    sourceFiles: [],
    proposedContent: message,
    status: 'pending',
  }
}

// Re-export PROMPT_VERSION for consumers that want to display the active version.
export { PROMPT_VERSION }
