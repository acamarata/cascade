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
//   This is the leaf-crate home for these constants (cascade-types is the DAG
//   root — every other Cascade crate depends on it, directly or transitively)
//   so the "no hardcoded model-ids" doctrine is enforceable across the whole
//   workspace, including crates (cascade-providers, cascade-mcp) that do not
//   depend on cascade-core.
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
///
/// Verified 2026-08-19: Claude Opus 5 is GA and is Anthropic's recommended
/// default for complex agentic coding. Claude Opus 4.8 is now listed under
/// "Legacy models" and was the previous value here.
/// Source: https://platform.claude.com/docs/en/about-claude/models/overview
pub const MODEL_CLAUDE_OPUS: &str = "claude-opus-5";

/// Previous Opus generation, retained for pinned/legacy callers only.
/// Legacy per Anthropic docs as of 2026-08-19 — do not use for new routing.
pub const MODEL_CLAUDE_OPUS_4_8: &str = "claude-opus-4-8";

/// T2 bulk-execution model — default for agent harness generation.
pub const MODEL_CLAUDE_SONNET: &str = "claude-sonnet-5";

/// T3 cheap triage model — grunt work, taxonomy, post-prompt hooks.
pub const MODEL_CLAUDE_HAIKU: &str = "claude-haiku-4-5";

/// Fable — Anthropic's most capable widely released model (GA 2026-06-09).
/// Verified 2026-08-19: NOT retired. Use for the highest-capability workloads;
/// $10/$50 per MTok vs Opus 5's $5/$25, so reserve it for genuine T1 work.
/// Source: https://platform.claude.com/docs/en/about-claude/models/overview
pub const MODEL_CLAUDE_FABLE: &str = "claude-fable-5";

// ── OpenAI / Codex ───────────────────────────────────────────────────────────

/// GPT T1 flagship model via Codex CLI (Sol).
pub const MODEL_GPT_SOL: &str = "gpt-5.6-sol";

/// GPT T2 balanced model via Codex CLI (Terra).
pub const MODEL_GPT_TERRA: &str = "gpt-5.6-terra";

/// GPT T3 fastest/cheapest tier via Codex CLI (Luna).
pub const MODEL_GPT_LUNA: &str = "gpt-5.6-luna";

/// GPT model via Codex CLI (default/flagship routing).
pub const MODEL_GPT: &str = MODEL_GPT_SOL;

// ── Google / Gemini ───────────────────────────────────────────────────────────

/// Gemini T2 pro model via AGY CLI.
///
/// UNVERIFIED as of 2026-08-19: Google's model list documents only
/// `gemini-3.1-pro-preview` (Preview); the bare `gemini-3.1-pro` id appears on
/// neither the models page nor the pricing page. Left unchanged pending
/// confirmation — do not route production traffic on the assumption it is GA.
/// Source: https://ai.google.dev/gemini-api/docs/models
pub const MODEL_GEMINI_PRO: &str = "gemini-3.1-pro";

/// Gemini T3 key-pool model for cheap grunt work (GFP pool).
///
/// Verified 2026-08-19: `gemini-3.7-flash` is Stable and cheaper than 3.5
/// ($0.75/$3.75 per MTok through 2026-12-31, vs 3.5-flash at $1.50/$9.00).
/// Prefer MODEL_GEMINI_FLASH_37 for new GF-pool routing.
/// Source: https://ai.google.dev/gemini-api/docs/pricing
pub const MODEL_GEMINI_FLASH: &str = "gemini-3.5-flash";

/// Gemini 3.7 Flash — Stable as of 2026-08-19, newer and cheaper than 3.5.
/// Source: https://ai.google.dev/gemini-api/docs/models
pub const MODEL_GEMINI_FLASH_37: &str = "gemini-3.7-flash";

/// Gemini Flash auto-tracking alias used by the GFP proxy (localhost:3761)
/// and the Anthropic-compat translation layer. Google's `-latest` aliases
/// always resolve to the current Flash version without needing a
/// retirement-driven edit. Mirrors the raw literals in
/// `cascade-daemon/src/proxy/anthropic_compat.rs:map_model()`.
/// Used by the frontend merge providerRouter and the Rust ai_service.
pub const MODEL_GEMINI_FLASH_LATEST: &str = "gemini-flash-latest";

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
