/**
 * Purpose: Provider router for the Cascade AI merge engine.
 *   Determines which AI provider is available and builds a typed Provider instance
 *   that the mergeService can call.
 *
 * Provider priority (per T-P3-E03-25 spec):
 *   1. GeminiPool  — proxy running at localhost:3761 (auto-detected via /health)
 *   2. Anthropic   — API key in wizard state or OAuth-connected via cascade_providers_oauth_status
 *   3. OpenAI      — API key in wizard state or OAuth-connected via cascade_providers_oauth_status
 *   4. Local model — detected via local_llm_detect Tauri command (Ollama/llama.cpp)
 *
 * Constraints:
 *   - No API keys stored here; credentials flow through ProviderRoutingContext.
 *   - Scoped to onboarding needs only — general-purpose routing is E-P7-13.
 *
 * SPORT: MASTER-COMPONENTS.md — providerRouter — merge/providerRouter.ts
 * Task: T-P3-E03-25 / T-P7-E09-07
 */

import { invoke } from '@tauri-apps/api/core'
import type { MergeRequest } from './types'

/** Identifies a provider by name (used in MergeResult.modelUsed). */
export type ProviderId = 'gemini-pool' | 'anthropic' | 'openai' | 'local'

/** A callable AI provider abstraction. */
export interface Provider {
  name: string
  call(request: MergeRequest): Promise<string>
}

/** Minimal view of WizardState needed for provider routing. */
export interface ProviderRoutingContext {
  /** True once the user connected at least one provider in step 2. */
  providerConnected: boolean
  /** Anthropic API key (if connected via API key in the wizard). */
  anthropicApiKey?: string
  /** OpenAI API key (if connected via API key in the wizard). */
  openaiApiKey?: string
  /** True if a local model was downloaded and is ready. */
  localModelReady?: boolean
}

// ── Gemini Pool provider ─────────────────────────────────────────────────────

const GEMINI_HEALTH_URL = 'http://127.0.0.1:3761/health'
// Mirrors cascade_types::model_ids::MODEL_GEMINI_FLASH_LATEST. Exported for mergeService.ts.
export const GEMINI_MODEL_ID = 'gemini-flash-latest'
const GEMINI_GENERATE_URL = `http://127.0.0.1:3761/v1beta/models/${GEMINI_MODEL_ID}:generateContent`
const HEALTH_TIMEOUT_MS = 800

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
      if (!resp.ok) throw new Error(`Gemini proxy returned HTTP ${resp.status}`)
      const json = (await resp.json()) as {
        candidates?: Array<{ content?: { parts?: Array<{ text?: string }> } }>
      }
      return json.candidates?.[0]?.content?.parts?.[0]?.text ?? ''
    },
  }
}

// ── Anthropic provider (Messages API) ─────────────────────────────────────────

const ANTHROPIC_MODEL_ID = 'claude-sonnet-4-5'
const ANTHROPIC_API_URL = 'https://api.anthropic.com/v1/messages'

function makeAnthropicProvider(apiKey: string): Provider {
  return {
    name: ANTHROPIC_MODEL_ID,
    async call(request: MergeRequest): Promise<string> {
      const resp = await fetch(ANTHROPIC_API_URL, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'x-api-key': apiKey,
          'anthropic-version': '2023-06-01',
        },
        body: JSON.stringify({
          model: ANTHROPIC_MODEL_ID,
          max_tokens: 8192,
          system: request.systemPrompt,
          messages: [{ role: 'user', content: request.userPrompt }],
        }),
      })
      if (!resp.ok) {
        const errBody = await resp.text().catch(() => '')
        throw new Error(`Anthropic API returned HTTP ${resp.status}: ${errBody}`)
      }
      const json = (await resp.json()) as {
        content?: Array<{ type: string; text?: string }>
      }
      return json.content?.filter((b) => b.type === 'text').map((b) => b.text ?? '').join('') ?? ''
    },
  }
}

// ── OpenAI provider (Chat Completions API) ────────────────────────────────────

const OPENAI_MODEL_ID = 'gpt-4o'
const OPENAI_API_URL = 'https://api.openai.com/v1/chat/completions'

function makeOpenAIProvider(apiKey: string): Provider {
  return {
    name: OPENAI_MODEL_ID,
    async call(request: MergeRequest): Promise<string> {
      const resp = await fetch(OPENAI_API_URL, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${apiKey}`,
        },
        body: JSON.stringify({
          model: OPENAI_MODEL_ID,
          messages: [
            { role: 'system', content: request.systemPrompt },
            { role: 'user', content: request.userPrompt },
          ],
        }),
      })
      if (!resp.ok) {
        const errBody = await resp.text().catch(() => '')
        throw new Error(`OpenAI API returned HTTP ${resp.status}: ${errBody}`)
      }
      const json = (await resp.json()) as {
        choices?: Array<{ message?: { content?: string } }>
      }
      return json.choices?.[0]?.message?.content ?? ''
    },
  }
}

// ── Local model provider (Ollama via local_llm_detect) ────────────────────────

const LOCAL_MODEL_ID = 'llama-3.2-3b'

interface LocalLlmDetectResult {
  type: string
  endpoint?: string
  detected_port?: number
}

async function detectLocalLlm(): Promise<LocalLlmDetectResult | null> {
  try {
    const result = await invoke<LocalLlmDetectResult>('local_llm_detect')
    if (result && result.type && result.type !== 'none') return result
  } catch {
    // local_llm_detect unavailable — non-blocking
  }
  return null
}

function makeLocalProvider(endpoint: string): Provider {
  return {
    name: LOCAL_MODEL_ID,
    async call(request: MergeRequest): Promise<string> {
      const resp = await fetch(`${endpoint}/api/generate`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          model: LOCAL_MODEL_ID,
          prompt: request.userPrompt,
          system: request.systemPrompt,
          stream: false,
        }),
      })
      if (!resp.ok) throw new Error(`Local LLM at ${endpoint} returned HTTP ${resp.status}`)
      const json = (await resp.json()) as { response?: string }
      return json.response ?? ''
    },
  }
}

// ── Credential accessors + OAuth check ────────────────────────────────────────

function getAnthropicApiKey(ctx: ProviderRoutingContext): string | null {
  return ctx.anthropicApiKey ?? null
}

function getOpenAIApiKey(ctx: ProviderRoutingContext): string | null {
  return ctx.openaiApiKey ?? null
}

/** Check if a provider is OAuth-connected via cascade_providers_oauth_status. */
async function isOAuthConnected(providerId: string): Promise<boolean> {
  try {
    const status = await invoke<string>('cascade_providers_oauth_status', { id: providerId })
    return status === 'connected'
  } catch {
    return false
  }
}

// ── Public API ────────────────────────────────────────────────────────────────

/**
 * Determine the active AI provider from the wizard context.
 * Returns the first available provider in priority order, or null if none.
 */
export async function getActiveProvider(ctx: ProviderRoutingContext): Promise<Provider | null> {
  if (!ctx.providerConnected) return null

  // 1. GeminiPool (localhost proxy — highest priority; no credentials needed)
  if (await isGeminiPoolAvailable()) return makeGeminiPoolProvider()

  // 2. Anthropic — API key in context OR OAuth-connected via daemon
  const anthropicKey = getAnthropicApiKey(ctx)
  if (anthropicKey) return makeAnthropicProvider(anthropicKey)
  if (await isOAuthConnected('anthropic')) {
    // OAuth-connected: daemon has the token. Actual AI call through the daemon
    // is E-P7-13's scope; for onboarding, surface the provider as available.
    return makeAnthropicProvider('')
  }

  // 3. OpenAI — API key in context OR OAuth-connected via daemon
  const openaiKey = getOpenAIApiKey(ctx)
  if (openaiKey) return makeOpenAIProvider(openaiKey)
  if (await isOAuthConnected('openai')) return makeOpenAIProvider('')

  // 4. Local model — detected via local_llm_detect Tauri command
  if (ctx.localModelReady) {
    const localLlm = await detectLocalLlm()
    if (localLlm?.endpoint) return makeLocalProvider(localLlm.endpoint)
  }

  return null
}