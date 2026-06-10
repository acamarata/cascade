/**
 * Purpose: Provider router for the Cascade AI merge engine.
 *   Determines which AI provider is available and builds a typed Provider instance
 *   that the mergeService can call.
 *
 * Provider priority (per T-P3-E03-25 spec):
 *   1. GeminiPool  — proxy running at localhost:3761 (auto-detected via /health)
 *   2. Anthropic   — API key present in wizard state (stub: E-04 fills credential flow)
 *   3. OpenAI      — API key present in wizard state (stub: E-04 fills credential flow)
 *   4. Local model — downloaded local model (stub: E-04 implements cascade_local_model command)
 *
 * Inputs:  WizardState (providerConnected flag + future provider credential fields).
 * Outputs: Provider | null — null means no provider available.
 *
 * Constraints:
 *   - No API keys stored here; credentials flow through WizardState / E-04 OAuth.
 *   - GeminiPool detection uses a fast health-check fetch (800 ms timeout).
 *   - All provider calls return a raw string (the model's text response).
 *   - Retry logic lives in mergeService.ts, not here.
 *
 * SPORT: MASTER-COMPONENTS.md — providerRouter — merge/providerRouter.ts
 * Task: T-P3-E03-25
 */

import type { MergeRequest } from './types'

// ---------------------------------------------------------------------------
// Provider interface
// ---------------------------------------------------------------------------

/** Identifies a provider by name (used in MergeResult.modelUsed). */
export type ProviderId = 'gemini-pool' | 'anthropic' | 'openai' | 'local'

/**
 * A callable AI provider abstraction.
 *
 * @param name - Provider/model identifier (stored in MergeResult.modelUsed).
 * @param call - Sends a MergeRequest to the provider and returns the raw model text.
 */
export interface Provider {
  name: string
  call(request: MergeRequest): Promise<string>
}

// ---------------------------------------------------------------------------
// WizardState shape (minimal — only the fields this module needs)
// ---------------------------------------------------------------------------

/** Minimal view of WizardState needed for provider routing. */
export interface ProviderRoutingContext {
  /** True once the user connected at least one provider in step 2. */
  providerConnected: boolean
  // E-04 will add: anthropicApiKey?: string, openaiApiKey?: string, localModelReady?: boolean
}

// ---------------------------------------------------------------------------
// Gemini Pool provider
// ---------------------------------------------------------------------------

const GEMINI_HEALTH_URL = 'http://127.0.0.1:3761/health'
const GEMINI_GENERATE_URL =
  'http://127.0.0.1:3761/v1beta/models/gemini-2.0-flash:generateContent'
const GEMINI_MODEL_ID = 'gemini-2.0-flash'
const HEALTH_TIMEOUT_MS = 800

/** Returns true if the Gemini proxy is reachable. */
async function isGeminiPoolAvailable(): Promise<boolean> {
  try {
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), HEALTH_TIMEOUT_MS)
    const resp = await fetch(GEMINI_HEALTH_URL, { signal: controller.signal })
    clearTimeout(timer)
    return resp.ok
  } catch {
    return false
  }
}

/** Build a GeminiPool provider instance. */
function makeGeminiPoolProvider(): Provider {
  return {
    name: GEMINI_MODEL_ID,
    async call(request: MergeRequest): Promise<string> {
      const body = {
        contents: [{ role: 'user', parts: [{ text: request.userPrompt }] }],
        systemInstruction: { parts: [{ text: request.systemPrompt }] },
        generationConfig: { responseMimeType: 'application/json' },
      }

      const resp = await fetch(GEMINI_GENERATE_URL, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })

      if (!resp.ok) {
        throw new Error(`Gemini proxy returned HTTP ${resp.status}`)
      }

      const json = (await resp.json()) as {
        candidates?: Array<{
          content?: { parts?: Array<{ text?: string }> }
        }>
      }

      const text =
        json.candidates?.[0]?.content?.parts?.[0]?.text ?? ''
      return text
    },
  }
}

// ---------------------------------------------------------------------------
// Stub providers (E-04 fills real implementations)
// ---------------------------------------------------------------------------

/** Anthropic API stub — E-04 fills OAuth + messages.create. */
function makeAnthropicProvider(_apiKey: string): Provider {
  return {
    name: 'claude-3-5-sonnet',
    async call(_request: MergeRequest): Promise<string> {
      throw new Error(
        'Anthropic provider not yet implemented. Connect via E-04 OAuth flow.',
      )
    },
  }
}

/** OpenAI API stub — E-04 fills OAuth + chat.completions.create. */
function makeOpenAIProvider(_apiKey: string): Provider {
  return {
    name: 'gpt-4o',
    async call(_request: MergeRequest): Promise<string> {
      throw new Error(
        'OpenAI provider not yet implemented. Connect via E-04 OAuth flow.',
      )
    },
  }
}

/** Local model stub — E-04 implements cascade_local_model Tauri command. */
function makeLocalProvider(): Provider {
  return {
    name: 'local-model',
    async call(_request: MergeRequest): Promise<string> {
      throw new Error(
        'Local model provider not yet implemented. E-04 will implement the Tauri command.',
      )
    },
  }
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Determine the active AI provider from the wizard context.
 *
 * Returns the first available provider in priority order, or null if none.
 * Performs an async health check for GeminiPool.
 *
 * @param ctx - Minimal wizard state needed for routing.
 */
export async function getActiveProvider(
  ctx: ProviderRoutingContext,
): Promise<Provider | null> {
  if (!ctx.providerConnected) {
    return null
  }

  // 1. GeminiPool (localhost proxy — highest priority; no credentials needed)
  if (await isGeminiPoolAvailable()) {
    return makeGeminiPoolProvider()
  }

  // 2. Anthropic API key (stub until E-04)
  const anthropicKey = getAnthropicApiKey(ctx)
  if (anthropicKey) {
    return makeAnthropicProvider(anthropicKey)
  }

  // 3. OpenAI API key (stub until E-04)
  const openaiKey = getOpenAIApiKey(ctx)
  if (openaiKey) {
    return makeOpenAIProvider(openaiKey)
  }

  // 4. Local model (stub until E-04)
  if (isLocalModelReady(ctx)) {
    return makeLocalProvider()
  }

  return null
}

// ---------------------------------------------------------------------------
// Credential accessors (stubs — E-04 populates WizardState with real keys)
// ---------------------------------------------------------------------------

/** Extract Anthropic API key from wizard state (E-04 adds this field). */
function getAnthropicApiKey(ctx: ProviderRoutingContext): string | null {
  // E-04: return (ctx as WizardStateWithCredentials).anthropicApiKey ?? null
  void ctx
  return null
}

/** Extract OpenAI API key from wizard state (E-04 adds this field). */
function getOpenAIApiKey(ctx: ProviderRoutingContext): string | null {
  // E-04: return (ctx as WizardStateWithCredentials).openaiApiKey ?? null
  void ctx
  return null
}

/** Check if a local model is downloaded and ready (E-04 adds this field). */
function isLocalModelReady(ctx: ProviderRoutingContext): boolean {
  // E-04: return (ctx as WizardStateWithCredentials).localModelReady ?? false
  void ctx
  return false
}
