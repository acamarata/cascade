//! Token cost estimation for AI provider calls.
//!
//! # Purpose
//!
//! Provides a static pricing table (snapshot dated 2026-05-31 — prices are
//! approximate and may drift; treat as estimates only) and a [`compute_cost`]
//! function that converts a [`TokenUsage`] into an estimated USD cost for a
//! given `(provider_id, model_id)` pair.
//!
//! # Inputs / Outputs
//!
//! - Input: `provider_id: &str`, `model_id: &str`, `usage: &TokenUsage`
//! - Output: `Option<f64>` — `Some(usd)` when the model is in the table,
//!   `None` when the model is unknown and no estimate is possible.
//!   Local/offline models return `Some(0.0)`.
//!
//! # Constraints
//!
//! - The table is **static at compile time** — no HTTP pricing API.
//! - Prices are in **USD per 1 million tokens** (input / output separately).
//! - [`compute_cost`] is pure and infallible; callers need not handle errors.
//! - The table covers the top ~3 models per provider for the 11 cloud
//!   providers that Cascade supports.

use std::collections::HashMap;
use std::sync::OnceLock;

use crate::types::TokenUsage;

// ── ModelPricing ──────────────────────────────────────────────────────────────

/// Per-model pricing in USD per 1 million tokens.
///
/// # SPORT
///
/// Snapshot dated **2026-05-31**. Prices are approximate; verify against
/// provider pricing pages before using for billing decisions.
#[derive(Debug, Clone, PartialEq)]
pub struct ModelPricing {
    /// USD cost per 1 million input/prompt tokens.
    pub prompt_per_1m: f64,
    /// USD cost per 1 million output/completion tokens.
    pub completion_per_1m: f64,
}

impl ModelPricing {
    const fn new(prompt_per_1m: f64, completion_per_1m: f64) -> Self {
        Self {
            prompt_per_1m,
            completion_per_1m,
        }
    }
}

// ── CostTable ─────────────────────────────────────────────────────────────────

/// A map from `(provider_id, model_id)` to [`ModelPricing`].
///
/// Populated once via [`OnceLock`]; all lookups after the first call are
/// allocation-free.
pub type CostTable = HashMap<(&'static str, &'static str), ModelPricing>;

/// Initialise and return the global static pricing table.
///
/// Prices are a snapshot as of **2026-05-31**. This function is called at most
/// once; subsequent calls return the cached table.
pub fn global_cost_table() -> &'static CostTable {
    static TABLE: OnceLock<CostTable> = OnceLock::new();
    TABLE.get_or_init(build_table)
}

/// Build the pricing table.  All prices are in USD / 1 M tokens (input,
/// output).  Snapshot date: **2026-05-31**.
///
/// Sources used for this snapshot:
/// - Anthropic: <https://www.anthropic.com/pricing>
/// - OpenAI: <https://openai.com/pricing>
/// - Google Gemini: <https://ai.google.dev/pricing>
/// - Mistral: <https://mistral.ai/technology/#pricing>
/// - Groq: <https://wow.groq.com/pricing/>
/// - Together: <https://www.together.ai/pricing>
/// - OpenRouter: pass-through at ~provider rates (no markup assumed here)
/// - Cohere: <https://cohere.com/pricing>
/// - DeepSeek: <https://platform.deepseek.com/api-docs/pricing>
/// - Ollama / local: zero (no cloud cost)
fn build_table() -> CostTable {
    let mut t: CostTable = HashMap::new();

    // ── Anthropic ─────────────────────────────────────────────────────────────
    // https://www.anthropic.com/pricing — snapshot 2026-05-31
    t.insert(
        ("anthropic", "claude-opus-4-8"),
        ModelPricing::new(15.0, 75.0),
    );
    t.insert(
        ("anthropic", "claude-sonnet-5"),
        ModelPricing::new(3.0, 15.0),
    );
    t.insert(
        ("anthropic", "claude-sonnet-4-6"),
        ModelPricing::new(3.0, 15.0),
    );
    t.insert(
        ("anthropic", "claude-haiku-3-5"),
        ModelPricing::new(0.8, 4.0),
    );
    // Legacy aliases still in wide use
    t.insert(
        ("anthropic", "claude-3-5-sonnet-20241022"),
        ModelPricing::new(3.0, 15.0),
    );
    t.insert(
        ("anthropic", "claude-3-5-haiku-20241022"),
        ModelPricing::new(0.8, 4.0),
    );
    t.insert(
        ("anthropic", "claude-3-opus-20240229"),
        ModelPricing::new(15.0, 75.0),
    );

    // ── OpenAI ────────────────────────────────────────────────────────────────
    // https://openai.com/pricing — snapshot 2026-05-31
    t.insert(("openai", "gpt-4.1"), ModelPricing::new(2.0, 8.0));
    t.insert(("openai", "gpt-4.1-mini"), ModelPricing::new(0.4, 1.6));
    t.insert(("openai", "gpt-4o"), ModelPricing::new(2.5, 10.0));
    t.insert(("openai", "gpt-4o-mini"), ModelPricing::new(0.15, 0.6));
    t.insert(("openai", "o3"), ModelPricing::new(10.0, 40.0));
    t.insert(("openai", "o4-mini"), ModelPricing::new(1.1, 4.4));

    // ── Google Gemini ─────────────────────────────────────────────────────────
    // https://ai.google.dev/pricing — snapshot 2026-05-31
    t.insert(
        ("google-gemini", "gemini-2.5-pro"),
        ModelPricing::new(1.25, 10.0),
    );
    t.insert(
        ("google-gemini", "gemini-2.5-flash"),
        ModelPricing::new(0.075, 0.3),
    );
    t.insert(
        ("google-gemini", "gemini-2.0-flash"),
        ModelPricing::new(0.1, 0.4),
    );
    t.insert(
        ("google-gemini", "gemini-1.5-pro"),
        ModelPricing::new(1.25, 5.0),
    );
    t.insert(
        ("google-gemini", "gemini-1.5-flash"),
        ModelPricing::new(0.075, 0.3),
    );

    // ── OpenCode (OpenAI-compatible endpoint — pass-through pricing) ──────────
    // OpenCode routes to underlying models; we mirror OpenAI rates.
    t.insert(("opencode", "gpt-4o"), ModelPricing::new(2.5, 10.0));
    t.insert(("opencode", "gpt-4o-mini"), ModelPricing::new(0.15, 0.6));
    t.insert(("opencode", "o3"), ModelPricing::new(10.0, 40.0));

    // ── OpenRouter ────────────────────────────────────────────────────────────
    // https://openrouter.ai/models — approximate median rates, snapshot 2026-05-31
    t.insert(
        ("openrouter", "anthropic/claude-sonnet-4-6"),
        ModelPricing::new(3.0, 15.0),
    );
    t.insert(
        ("openrouter", "openai/gpt-4o"),
        ModelPricing::new(2.5, 10.0),
    );
    t.insert(
        ("openrouter", "google/gemini-2.5-pro"),
        ModelPricing::new(1.25, 10.0),
    );
    t.insert(
        ("openrouter", "meta-llama/llama-3.3-70b-instruct"),
        ModelPricing::new(0.59, 0.79),
    );

    // ── Together AI ───────────────────────────────────────────────────────────
    // https://www.together.ai/pricing — snapshot 2026-05-31
    t.insert(
        ("together", "meta-llama/Llama-3.3-70B-Instruct-Turbo"),
        ModelPricing::new(0.88, 0.88),
    );
    t.insert(
        ("together", "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo"),
        ModelPricing::new(0.18, 0.18),
    );
    t.insert(
        ("together", "mistralai/Mixtral-8x7B-Instruct-v0.1"),
        ModelPricing::new(0.6, 0.6),
    );

    // ── Groq ──────────────────────────────────────────────────────────────────
    // https://wow.groq.com/pricing/ — snapshot 2026-05-31
    t.insert(
        ("groq", "llama-3.3-70b-versatile"),
        ModelPricing::new(0.59, 0.79),
    );
    t.insert(
        ("groq", "llama-3.1-8b-instant"),
        ModelPricing::new(0.05, 0.08),
    );
    t.insert(
        ("groq", "mixtral-8x7b-32768"),
        ModelPricing::new(0.24, 0.24),
    );
    t.insert(("groq", "gemma2-9b-it"), ModelPricing::new(0.2, 0.2));

    // ── Mistral ───────────────────────────────────────────────────────────────
    // https://mistral.ai/technology/#pricing — snapshot 2026-05-31
    t.insert(
        ("mistral", "mistral-large-latest"),
        ModelPricing::new(2.0, 6.0),
    );
    t.insert(
        ("mistral", "mistral-small-latest"),
        ModelPricing::new(0.1, 0.3),
    );
    t.insert(("mistral", "codestral-latest"), ModelPricing::new(0.3, 0.9));
    t.insert(
        ("mistral", "open-mistral-7b"),
        ModelPricing::new(0.25, 0.25),
    );

    // ── DeepSeek ──────────────────────────────────────────────────────────────
    // https://platform.deepseek.com/api-docs/pricing — snapshot 2026-05-31
    t.insert(("deepseek", "deepseek-chat"), ModelPricing::new(0.27, 1.1));
    t.insert(
        ("deepseek", "deepseek-reasoner"),
        ModelPricing::new(0.55, 2.19),
    );
    t.insert(
        ("deepseek", "deepseek-coder"),
        ModelPricing::new(0.14, 0.28),
    );

    // ── Cohere ────────────────────────────────────────────────────────────────
    // https://cohere.com/pricing — snapshot 2026-05-31
    t.insert(("cohere", "command-r-plus"), ModelPricing::new(2.5, 10.0));
    t.insert(("cohere", "command-r"), ModelPricing::new(0.15, 0.6));
    t.insert(("cohere", "command-light"), ModelPricing::new(0.3, 0.6));

    // ── Local / Ollama ────────────────────────────────────────────────────────
    // Local models have no cloud cost; always Some(0.0) via zero-rate entries.
    t.insert(("ollama", "qwen2.5-coder:7b"), ModelPricing::new(0.0, 0.0));
    t.insert(("ollama", "llama3.2:3b"), ModelPricing::new(0.0, 0.0));
    t.insert(("ollama", "llama3.1:8b"), ModelPricing::new(0.0, 0.0));
    t.insert(("ollama", "codestral:22b"), ModelPricing::new(0.0, 0.0));

    t
}

// ── compute_cost ──────────────────────────────────────────────────────────────

/// Estimate the cost in USD for a completed call.
///
/// # Arguments
///
/// * `provider` – the provider's stable ID (e.g. `"anthropic"`, `"openai"`).
/// * `model`    – the model ID exactly as returned by the provider (e.g.
///   `"claude-sonnet-4-6"`, `"gpt-4o"`).
/// * `usage`    – token counts from [`CompletionResponse::usage`].
///
/// # Returns
///
/// * `Some(usd)` when the model is in the pricing table (including local
///   models, which return `Some(0.0)`).
/// * `None` when the model is not in the table and no estimate is possible.
///   Callers should surface an "unknown model — cost unavailable" warning.
///
/// # Example
///
/// ```rust
/// use cascade_providers::cost::compute_cost;
/// use cascade_providers::types::TokenUsage;
///
/// let usage = TokenUsage { prompt_tokens: 1000, completion_tokens: 500, total_tokens: 1500 };
/// let cost = compute_cost("anthropic", "claude-sonnet-4-6", &usage);
/// assert!(cost.unwrap() > 0.0);
/// ```
pub fn compute_cost(provider: &str, model: &str, usage: &TokenUsage) -> Option<f64> {
    let table = global_cost_table();
    let pricing = table.get(&(provider, model))?;
    let cost = (usage.prompt_tokens as f64 / 1_000_000.0) * pricing.prompt_per_1m
        + (usage.completion_tokens as f64 / 1_000_000.0) * pricing.completion_per_1m;
    Some(cost)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::TokenUsage;

    fn usage(prompt: u32, completion: u32) -> TokenUsage {
        TokenUsage {
            prompt_tokens: prompt,
            completion_tokens: completion,
            total_tokens: prompt + completion,
        }
    }

    // --- Positive lookups ---

    #[test]
    fn anthropic_sonnet_known_price() {
        // claude-sonnet-4-6: $3.00 / 1M prompt, $15.00 / 1M completion
        // 1000 prompt + 500 completion = 0.001 * 3.0 + 0.0005 * 15.0 = 0.003 + 0.0075 = 0.0105
        let cost = compute_cost("anthropic", "claude-sonnet-4-6", &usage(1000, 500));
        let c = cost.expect("should return Some for known model");
        assert!((c - 0.0105).abs() < 1e-10, "expected 0.0105, got {c}");
    }

    #[test]
    fn openai_gpt4o_known_price() {
        // gpt-4o: $2.50 / 1M prompt, $10.00 / 1M completion
        // 2000 prompt + 1000 completion = 0.002 * 2.5 + 0.001 * 10.0 = 0.005 + 0.010 = 0.015
        let cost = compute_cost("openai", "gpt-4o", &usage(2000, 1000));
        let c = cost.expect("gpt-4o should be in table");
        assert!((c - 0.015).abs() < 1e-10, "expected 0.015, got {c}");
    }

    #[test]
    fn google_gemini_flash_known_price() {
        // gemini-2.5-flash: $0.075 / 1M prompt, $0.30 / 1M completion
        // 4000 prompt + 2000 completion = 0.004 * 0.075 + 0.002 * 0.3 = 0.0003 + 0.0006 = 0.0009
        let cost = compute_cost("google-gemini", "gemini-2.5-flash", &usage(4000, 2000));
        let c = cost.expect("gemini-2.5-flash should be in table");
        assert!((c - 0.0009).abs() < 1e-10, "expected 0.0009, got {c}");
    }

    #[test]
    fn deepseek_chat_known_price() {
        // deepseek-chat: $0.27 / 1M prompt, $1.10 / 1M completion
        let cost = compute_cost("deepseek", "deepseek-chat", &usage(1_000_000, 1_000_000));
        let c = cost.expect("deepseek-chat in table");
        // 1M + 1M = 0.27 + 1.10 = 1.37
        assert!((c - 1.37).abs() < 1e-9, "expected 1.37, got {c}");
    }

    #[test]
    fn groq_llama_known_price() {
        let cost = compute_cost("groq", "llama-3.3-70b-versatile", &usage(1000, 1000));
        let c = cost.expect("groq llama in table");
        // 0.001 * 0.59 + 0.001 * 0.79 = 0.00059 + 0.00079 = 0.00138
        assert!((c - 0.00138).abs() < 1e-10, "expected 0.00138, got {c}");
    }

    // --- Local model: zero cost, not None ---

    #[test]
    fn local_ollama_returns_zero_not_none() {
        let cost = compute_cost("ollama", "qwen2.5-coder:7b", &usage(1000, 500));
        assert_eq!(cost, Some(0.0), "local model should return Some(0.0)");
    }

    #[test]
    fn local_ollama_llama_returns_zero() {
        let cost = compute_cost("ollama", "llama3.2:3b", &usage(500, 200));
        assert_eq!(cost, Some(0.0));
    }

    // --- Unknown model: None ---

    #[test]
    fn unknown_model_returns_none() {
        let cost = compute_cost("anthropic", "claude-99-unknown", &usage(1000, 500));
        assert_eq!(cost, None, "unknown model should return None");
    }

    #[test]
    fn unknown_provider_returns_none() {
        let cost = compute_cost("nonexistent-provider", "some-model", &usage(100, 50));
        assert_eq!(cost, None);
    }

    // --- Zero-token usage ---

    #[test]
    fn zero_usage_known_model_returns_zero_cost() {
        let cost = compute_cost("openai", "gpt-4o", &usage(0, 0));
        assert_eq!(cost, Some(0.0));
    }

    // --- Cohere ---

    #[test]
    fn cohere_command_r_plus_known_price() {
        // $2.50 / 1M prompt, $10.00 / 1M completion
        let cost = compute_cost("cohere", "command-r-plus", &usage(1_000_000, 500_000));
        let c = cost.expect("cohere command-r-plus in table");
        // 1.0 * 2.5 + 0.5 * 10.0 = 2.5 + 5.0 = 7.5
        assert!((c - 7.5).abs() < 1e-9, "expected 7.5, got {c}");
    }

    // --- Mistral ---

    #[test]
    fn mistral_large_known_price() {
        let cost = compute_cost(
            "mistral",
            "mistral-large-latest",
            &usage(1_000_000, 1_000_000),
        );
        let c = cost.expect("mistral-large in table");
        // 1.0 * 2.0 + 1.0 * 6.0 = 8.0
        assert!((c - 8.0).abs() < 1e-9, "expected 8.0, got {c}");
    }

    // --- openrouter ---

    #[test]
    fn openrouter_sonnet_known_price() {
        let cost = compute_cost(
            "openrouter",
            "anthropic/claude-sonnet-4-6",
            &usage(1000, 500),
        );
        assert!(cost.is_some());
        assert!(cost.unwrap() > 0.0);
    }
}
