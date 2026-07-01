// model_ids.rs — Canonical fleet model-ID string constants.
//
// Purpose: Single source of truth for the model-ID strings used by Cascade's
//   code-generation and harness layers. Consumers (agents_md, harness_files,
//   etc.) import these constants instead of duplicating raw string literals.
//
//   The fleet routing data lives in `accounts_store::build_model_matrix()` and
//   `data/model-matrix.json`; these constants MUST match those definitions.
//   Provider adapters (anthropic.rs, gemini.rs, cost.rs) and runtime fallback
//   tables (chat_handlers, sampling.rs) each document their own model choices
//   independently — they are NOT duplicates; they serve different roles (API
//   negotiation vs. fleet routing vs. code generation defaults).
//
// Inputs:  None — compile-time constants only.
// Outputs: `&'static str` constants imported by callers.
//
// Constraints:
//   - Keep in sync with `accounts_store::build_model_matrix()` entries.
//   - No runtime allocation; no serde; no dependencies.
//   - Update this file whenever accounts_store model IDs change.
//
// SPORT: MASTER-COMPONENTS.md — model_ids constants — model_ids.rs

// ── Anthropic / Claude CC models ─────────────────────────────────────────────

/// T1 planning model — high-stakes synthesis and final gates.
pub const MODEL_CLAUDE_OPUS: &str = "claude-opus-4-8";

/// T2 bulk-execution model — default for agent harness generation.
pub const MODEL_CLAUDE_SONNET: &str = "claude-sonnet-5";

/// T3 cheap triage model — grunt work, taxonomy, post-prompt hooks.
pub const MODEL_CLAUDE_HAIKU: &str = "claude-haiku-4-5";

/// Fable — extra CC account, hardest-reasoning tasks.
pub const MODEL_CLAUDE_FABLE: &str = "claude-fable-5";

// ── OpenAI / Codex ───────────────────────────────────────────────────────────

/// GPT T2 model via Codex CLI.
pub const MODEL_GPT: &str = "gpt-5.5";

// ── Google / Gemini ───────────────────────────────────────────────────────────

/// Gemini T2 pro model via AGY CLI.
pub const MODEL_GEMINI_PRO: &str = "gemini-3.1-pro";

/// Gemini T3 key-pool model for cheap grunt work (GFP pool).
pub const MODEL_GEMINI_FLASH: &str = "gemini-3.5-flash";

// ── OpenCode / GLM ───────────────────────────────────────────────────────────

/// GLM T2 model via OpenCode Run.
pub const MODEL_GLM: &str = "glm-5.2";

// ── Convenience alias ─────────────────────────────────────────────────────────

/// Default model for generated harness files (AGENTS.md, opencode.json, etc.).
///
/// Maps to the T2 Claude Sonnet entry — the workhorse model used by Codex and
/// OpenCode harnesses. Must match the `claude-sonnet-*` entry in
/// `accounts_store::build_model_matrix()`.
pub const DEFAULT_HARNESS_MODEL: &str = MODEL_CLAUDE_SONNET;
