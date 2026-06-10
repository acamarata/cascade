/**
 * Purpose: TypeScript types for the AI provider layer in Cascade.
 *   Mirrors the Rust types in crates/cascade-types/src/providers.rs (spec'd in T-P3-E04-26).
 * Inputs:  Returned by cascade_providers_list, cascade_providers_health IPC commands.
 * Outputs: Exported interfaces consumed by ProviderSettings page + AddProviderDialog.
 * Constraints:
 *   - Schema is additive-only — never rename or remove without a versioning ticket.
 *   - `authMethod` and `status` are exhaustive discriminants; add new variants via ticket.
 * SPORT: MASTER-LIBS.md — providers types, src/types/providers.ts — T-P3-E04-23
 */

// ── Core provider types ───────────────────────────────────────────────────────

/** Auth method used by a provider. */
export type ProviderAuthMethod = 'api_key' | 'oauth' | 'none'

/** Connection status reported by the daemon health check. */
export type ProviderStatus = 'healthy' | 'unhealthy' | 'unknown'

/**
 * A provider entry as returned by cascade_providers_list.
 * Mirrors ProviderListItem in crates/cascade-types.
 */
export interface ProviderListItem {
  /** Stable provider id, e.g. "anthropic", "gemini", "openai". */
  id: string
  /** Human-readable display name, e.g. "Anthropic (Claude)". */
  name: string
  /** Auth method this provider uses. */
  authMethod: ProviderAuthMethod
  /** Last health-check result from the daemon. */
  status: ProviderStatus
  /** Optional error message when status === 'unhealthy'. */
  errorMsg?: string
  /** Estimated USD cost incurred today (may be undefined if daemon unreachable). */
  todayCostUsd?: number
}

// ── Static provider catalogue (used by AddProviderDialog) ───────────────────

/**
 * Entry in the static list of all supported providers shown in the picker.
 * Not tied to whether a provider is configured — purely UI metadata.
 */
export interface ProviderCatalogEntry {
  id: string
  name: string
  authMethod: ProviderAuthMethod
  /** Short description shown in the picker. */
  description: string
}

/**
 * All providers the Cascade app can connect to.
 * Kept in display order.  Generic endpoint is always last.
 */
export const PROVIDER_CATALOG: ProviderCatalogEntry[] = [
  {
    id: 'anthropic',
    name: 'Anthropic (Claude)',
    authMethod: 'api_key',
    description: 'Claude 3.5 Sonnet, Claude Opus — API key',
  },
  {
    id: 'openai',
    name: 'OpenAI (ChatGPT)',
    authMethod: 'api_key',
    description: 'GPT-4o, o1 — API key',
  },
  {
    id: 'groq',
    name: 'Groq',
    authMethod: 'api_key',
    description: 'Llama 3.3, Mixtral — API key',
  },
  {
    id: 'mistral',
    name: 'Mistral',
    authMethod: 'api_key',
    description: 'Mistral Large, Codestral — API key',
  },
  {
    id: 'together',
    name: 'Together AI',
    authMethod: 'api_key',
    description: 'Llama, Qwen, FLUX — API key',
  },
  {
    id: 'xai',
    name: 'xAI (Grok)',
    authMethod: 'api_key',
    description: 'Grok-2 — API key',
  },
  {
    id: 'cohere',
    name: 'Cohere',
    authMethod: 'api_key',
    description: 'Command R+ — API key',
  },
  {
    id: 'perplexity',
    name: 'Perplexity',
    authMethod: 'api_key',
    description: 'Sonar, Sonar Pro — API key',
  },
  {
    id: 'gemini',
    name: 'Google Gemini',
    authMethod: 'oauth',
    description: 'Gemini 2.0 Flash, Pro — OAuth',
  },
  {
    id: 'opencode',
    name: 'OpenCode',
    authMethod: 'oauth',
    description: 'OpenCode session — OAuth',
  },
  {
    id: 'ollama',
    name: 'Ollama (local)',
    authMethod: 'none',
    description: 'Any local Ollama model — no auth',
  },
  {
    id: 'generic',
    name: 'Generic OpenAI-compatible',
    authMethod: 'api_key',
    description: 'Any OpenAI-compatible endpoint',
  },
]
