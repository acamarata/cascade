/**
 * Purpose: OAuth/API-key provider abstraction for all AI providers.
 * Inputs: Provider ID, OAuth scopes or API key string.
 * Outputs: Connected provider record; credentials stored in OS keychain.
 * Constraints: PKCE for OAuth providers. API key validation for key-based providers.
 *   Credentials never written to .cascade/ — OS keychain only (keyring crate via Tauri).
 *   Implements E10 security model: state param, timeout, token storage.
 * SPORT: E5-S05 — OAuth provider framework
 */

import { invoke } from "@tauri-apps/api/core";

// ---------------------------------------------------------------------------
// Provider registry
// ---------------------------------------------------------------------------

export type OAuthFlow = "pkce" | "api_key" | "config_detect";

export interface ProviderDef {
  id: string;
  label: string;
  flow: OAuthFlow;
  /** OAuth: authorization endpoint base URL */
  auth_url?: string;
  /** OAuth: token endpoint */
  token_url?: string;
  /** OAuth: required scopes */
  scopes?: string[];
  /** API key: regex pattern for validation */
  key_pattern?: RegExp;
  /** API key: endpoint used to test connectivity */
  test_endpoint?: string;
  /** Link shown to help user create credentials */
  help_url: string;
}

export const PROVIDERS: Record<string, ProviderDef> = {
  gemini: {
    id: "gemini",
    label: "Google Gemini",
    flow: "pkce",
    auth_url: "https://accounts.google.com/o/oauth2/v2/auth",
    token_url: "https://oauth2.googleapis.com/token",
    scopes: ["https://www.googleapis.com/auth/generative-language"],
    help_url: "https://aistudio.google.com/app/apikey",
  },
  anthropic: {
    id: "anthropic",
    label: "Anthropic",
    flow: "api_key",
    key_pattern: /^sk-ant-[a-zA-Z0-9_-]{90,}$/,
    test_endpoint: "https://api.anthropic.com/v1/models",
    help_url: "https://console.anthropic.com/settings/keys",
  },
  openai: {
    id: "openai",
    label: "OpenAI",
    flow: "api_key",
    key_pattern: /^sk-[a-zA-Z0-9_-]{48,}$/,
    test_endpoint: "https://api.openai.com/v1/models",
    help_url: "https://platform.openai.com/api-keys",
  },
  openrouter: {
    id: "openrouter",
    label: "OpenRouter",
    flow: "api_key",
    key_pattern: /^sk-or-v1-[a-zA-Z0-9_-]{60,}$/,
    test_endpoint: "https://openrouter.ai/api/v1/models",
    help_url: "https://openrouter.ai/keys",
  },
  opencode_go: {
    id: "opencode_go",
    label: "OpenCode Go",
    flow: "config_detect",
    help_url: "https://opencode.ai",
  },
  groq: {
    id: "groq",
    label: "Groq",
    flow: "api_key",
    key_pattern: /^gsk_[a-zA-Z0-9_-]{52,}$/,
    test_endpoint: "https://api.groq.com/openai/v1/models",
    help_url: "https://console.groq.com/keys",
  },
  together: {
    id: "together",
    label: "Together AI",
    flow: "api_key",
    key_pattern: /^[a-f0-9]{64}$/,
    test_endpoint: "https://api.together.xyz/v1/models",
    help_url: "https://api.together.ai/settings/api-keys",
  },
  mistral: {
    id: "mistral",
    label: "Mistral",
    flow: "api_key",
    key_pattern: /^[a-zA-Z0-9]{32}$/,
    test_endpoint: "https://api.mistral.ai/v1/models",
    help_url: "https://console.mistral.ai/api-keys/",
  },
  deepseek: {
    id: "deepseek",
    label: "DeepSeek",
    flow: "api_key",
    key_pattern: /^sk-[a-f0-9]{32}$/,
    test_endpoint: "https://api.deepseek.com/v1/models",
    help_url: "https://platform.deepseek.com/api_keys",
  },
  cohere: {
    id: "cohere",
    label: "Cohere",
    flow: "api_key",
    key_pattern: /^[a-zA-Z0-9]{40}$/,
    test_endpoint: "https://api.cohere.com/v2/models",
    help_url: "https://dashboard.cohere.com/api-keys",
  },
};

export type ProviderId = keyof typeof PROVIDERS;

// ---------------------------------------------------------------------------
// Connection results
// ---------------------------------------------------------------------------

export interface ConnectionSuccess {
  ok: true;
  provider_id: string;
  account_hint: string; // masked, never the full key
}

export interface ConnectionFailure {
  ok: false;
  provider_id: string;
  error: string;
  user_message: string;
}

export type ConnectionResult = ConnectionSuccess | ConnectionFailure;

// ---------------------------------------------------------------------------
// OAuth PKCE flow
// ---------------------------------------------------------------------------

/**
 * Purpose: Launch browser OAuth PKCE flow and return result.
 * Opens system browser, starts local callback server on a random port.
 * E10: includes state param, 5-minute timeout, token stored in OS keychain.
 */
export async function connectOAuth(providerId: string): Promise<ConnectionResult> {
  const def = PROVIDERS[providerId];
  if (!def || def.flow !== "pkce") {
    return { ok: false, provider_id: providerId, error: "not_oauth", user_message: "This provider does not use OAuth." };
  }
  try {
    const result: ConnectionSuccess | ConnectionFailure = await invoke("oauth_pkce_flow", {
      provider_id: providerId,
      auth_url: def.auth_url,
      token_url: def.token_url,
      scopes: def.scopes,
    });
    return result;
  } catch (err) {
    return {
      ok: false,
      provider_id: providerId,
      error: String(err),
      user_message: "OAuth flow failed. Check your network connection and try again.",
    };
  }
}

// ---------------------------------------------------------------------------
// API key flow
// ---------------------------------------------------------------------------

/**
 * Purpose: Validate API key format and test connectivity, then store in keychain.
 * Inputs: providerId, raw API key string.
 * Outputs: ConnectionResult — success includes masked account hint.
 */
export async function connectApiKey(providerId: string, rawKey: string): Promise<ConnectionResult> {
  const def = PROVIDERS[providerId];
  if (!def || def.flow !== "api_key") {
    return { ok: false, provider_id: providerId, error: "not_api_key", user_message: "This provider does not use API keys." };
  }

  // Format validation
  if (def.key_pattern && !def.key_pattern.test(rawKey.trim())) {
    return {
      ok: false,
      provider_id: providerId,
      error: "invalid_format",
      user_message: `The key format does not match ${def.label}'s expected pattern. Check for extra spaces or truncation.`,
    };
  }

  // Test connectivity + store
  try {
    const result: ConnectionSuccess | ConnectionFailure = await invoke("api_key_connect", {
      provider_id: providerId,
      api_key: rawKey.trim(),
      test_endpoint: def.test_endpoint,
    });
    return result;
  } catch (err) {
    return {
      ok: false,
      provider_id: providerId,
      error: String(err),
      user_message: "Could not verify the API key. Check the key and your network connection.",
    };
  }
}

// ---------------------------------------------------------------------------
// Config-detect flow (OpenCode Go)
// ---------------------------------------------------------------------------

/**
 * Purpose: Detect existing OpenCode Go config and populate provider without re-auth.
 */
export async function connectConfigDetect(providerId: string): Promise<ConnectionResult> {
  try {
    const result: ConnectionSuccess | ConnectionFailure = await invoke("config_detect_connect", { provider_id: providerId });
    return result;
  } catch (err) {
    return {
      ok: false,
      provider_id: providerId,
      error: String(err),
      user_message: "Could not detect existing configuration.",
    };
  }
}

// ---------------------------------------------------------------------------
// Gemini Pool detection
// ---------------------------------------------------------------------------

export interface GeminiPoolStatus {
  detected: boolean;
  port?: number;
  endpoint?: string;
}

/**
 * Purpose: Check for running Gemini Pool proxy at default port 3761 or via manifest.
 */
export async function detectGeminiPool(): Promise<GeminiPoolStatus> {
  try {
    return await invoke("gemini_pool_detect");
  } catch {
    return { detected: false };
  }
}

// ---------------------------------------------------------------------------
// Local LLM detection
// ---------------------------------------------------------------------------

export interface LocalLlmStatus {
  type: "ollama" | "llama_cpp" | "none";
  endpoint?: string;
  models?: string[];
  detected_port?: number;
}

/**
 * Purpose: Detect running Ollama or llama.cpp server on well-known ports.
 */
export async function detectLocalLlm(): Promise<LocalLlmStatus> {
  try {
    return await invoke("local_llm_detect");
  } catch {
    return { type: "none" };
  }
}

// ---------------------------------------------------------------------------
// Keychain
// ---------------------------------------------------------------------------

/** Remove a provider's credentials from the OS keychain. */
export async function disconnectProvider(providerId: string): Promise<void> {
  await invoke("keychain_delete", { service: `cascade:${providerId}` });
}
