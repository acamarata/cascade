//! # context_assembler
//!
//! Universal minimal+perfect context assembly layer (ctx-01).
//!
//! ## Purpose
//! Bridges raw retrieval hits with the `ContextOptimizer` pipeline by
//! applying role-based budget overrides and model-size adjustments before
//! the optimizer runs.  Callers declare a `role` and `model` string; the
//! assembler looks up the matching `ContextProfile`, halves the budget for
//! known small models, and delegates to `ContextOptimizer` for dedup +
//! windowing.
//!
//! ## Inputs
//! - `ContextRequest` — query, role, model, caller-supplied token_budget
//! - `Vec<String>`   — raw text chunks from retrieval (pre-ranked)
//!
//! ## Outputs
//! `AssembledContext` — the final chunks, token count, and effective role.
//!
//! ## Constraints
//! - No async state; `assemble()` is synchronous.
//! - Profile lookup is O(n) over the profile list (n ≤ ~20 — not hot-path).
//! - "Small model" heuristic: model string contains "haiku", "flash-lite",
//!   or "8b" (case-insensitive).  Budget is halved in that case.
//! - Chat role budget is always capped at 500 tokens regardless of profile.
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-mcp/tool/context_assembler

use cascade_rag::context::ContextOptimizer;
use cascade_types::RetrievalHit;
use serde::Deserialize;

// ── ContextProfile ────────────────────────────────────────────────────────────

/// One row in `data/context_profiles.toml`.
///
/// Defines the default token budget, retrieval depth (`k_chunks`), and a
/// human-readable tier label for a named role.
#[derive(Debug, Clone, Deserialize, PartialEq)]
pub struct ContextProfile {
    /// Role identifier (e.g. "chat", "build", "review", "research").
    pub role: String,
    /// Default max estimated tokens for this role.
    pub token_budget: usize,
    /// Max number of retrieval chunks to consider before windowing.
    pub k_chunks: usize,
    /// Human-readable tier label ("minimal" | "standard" | "expanded" | "full").
    pub tier: String,
}

// ── ContextRequest ────────────────────────────────────────────────────────────

/// Caller-supplied assembly request for a single `cascade.context_slice` call.
///
/// The assembler merges these fields with the matching `ContextProfile`
/// (role → profile lookup) to derive the effective budget.
#[derive(Debug, Clone)]
pub struct ContextRequest {
    /// Natural-language query (passed through to retrieval, stored in metadata).
    pub query: String,
    /// Role hint — matched against loaded profiles.  Unknown roles fall back to
    /// the "default" profile (or the caller-supplied `token_budget`).
    pub role: String,
    /// Model string for small-model detection (e.g. "claude-haiku-4-5").
    /// Pass an empty string when the model is unknown.
    pub model: String,
    /// Caller-supplied token budget from the MCP argument.  Used as-is when no
    /// profile is found; overridden by the profile when a match exists.
    pub token_budget: usize,
}

// ── AssembledContext ──────────────────────────────────────────────────────────

/// Output of `ContextAssembler::assemble`.
#[derive(Debug, Clone)]
pub struct AssembledContext {
    /// Final text chunks after budget trimming and dedup.
    pub chunks: Vec<String>,
    /// Estimated token count for the included chunks.
    pub tokens_used: usize,
    /// Effective role used for this assembly (may be "default" when the
    /// requested role had no matching profile).
    pub role: String,
    /// Tier label from the matched profile (or "caller" when no profile matched).
    pub tier: String,
}

// ── ContextAssembler ─────────────────────────────────────────────────────────

/// Role-aware wrapper around `ContextOptimizer`.
///
/// ## Example
/// ```rust
/// use cascade_mcp::tool::context_assembler::{ContextAssembler, ContextRequest};
///
/// let assembler = ContextAssembler::new();
/// let req = ContextRequest {
///     query: "how does the daemon start?".into(),
///     role: "chat".into(),
///     model: "claude-haiku-4-5".into(),
///     token_budget: 4096,
/// };
/// let raw = vec!["fn main() { run_daemon(); }".to_string()];
/// let ctx = assembler.assemble(&req, raw);
/// assert!(ctx.tokens_used <= 500, "chat role must be ≤500 tokens");
/// ```
pub struct ContextAssembler {
    profiles: Vec<ContextProfile>,
}

/// TOML envelope matching `data/context_profiles.toml`.
#[derive(Deserialize)]
struct ProfilesFile {
    profiles: Vec<ContextProfile>,
}

impl Default for ContextAssembler {
    fn default() -> Self {
        Self::new()
    }
}

impl ContextAssembler {
    /// Create an assembler with the built-in default profiles.
    ///
    /// Equivalent to `load_profiles` on the embedded default profile set.
    /// Use this when no profile file is available at runtime.
    pub fn new() -> Self {
        Self {
            profiles: default_profiles(),
        }
    }

    /// Load profiles from a TOML file on disk.
    ///
    /// Falls back to [`default_profiles`] if the file cannot be read or parsed,
    /// logging a warning but never failing.  The file must match the
    /// `[[profiles]]` array format in `data/context_profiles.toml`.
    ///
    /// # Errors
    /// Returns `Err` only when the TOML is present but structurally invalid
    /// (e.g. wrong type for a field).  Missing file → silent fallback.
    pub fn load_profiles(path: &str) -> Result<Self, String> {
        let content = match std::fs::read_to_string(path) {
            Ok(c) => c,
            Err(_) => {
                // File missing or unreadable — fall back to defaults.
                return Ok(Self {
                    profiles: default_profiles(),
                });
            }
        };

        let parsed: ProfilesFile =
            toml::from_str(&content).map_err(|e| format!("context_profiles.toml parse error: {e}"))?;

        Ok(Self {
            profiles: parsed.profiles,
        })
    }

    /// Return the number of loaded profiles.
    pub fn profile_count(&self) -> usize {
        self.profiles.len()
    }

    /// Assemble a context window from raw text chunks.
    ///
    /// Steps:
    /// 1. Look up the matching `ContextProfile` by `req.role`.
    /// 2. Derive the effective budget:
    ///    - Start with the profile's `token_budget` (or `req.token_budget`
    ///      when no profile matched).
    ///    - Halve it when `req.model` matches the small-model heuristic.
    ///    - Cap at 500 when `req.role` is "chat".
    /// 3. Convert raw `Vec<String>` into `RetrievalHit`s (score = 1.0, rank 0,
    ///    no path info) — this lets us reuse `ContextOptimizer`'s dedup +
    ///    window pipeline without duplicating that logic here.
    /// 4. Run `ContextOptimizer::optimize(budget)` → dedup → window.
    /// 5. Return `AssembledContext`.
    pub fn assemble(&self, req: &ContextRequest, raw: Vec<String>) -> AssembledContext {
        // 1. Profile lookup.
        let (base_budget, tier) = self
            .profiles
            .iter()
            .find(|p| p.role == req.role)
            .map(|p| (p.token_budget, p.tier.as_str()))
            .unwrap_or((req.token_budget, "caller"));

        // 2. Effective budget calculation.
        let mut effective_budget = base_budget;

        // Small-model heuristic: haiku, flash-lite, or 8b variants get half budget.
        if is_small_model(&req.model) {
            effective_budget /= 2;
        }

        // Chat role hard cap at 500 tokens.
        if req.role == "chat" {
            effective_budget = effective_budget.min(500);
        }

        // Never let budget drop below 64 tokens (degenerate edge case).
        effective_budget = effective_budget.max(64);

        // 3. Convert raw strings to RetrievalHit for optimizer reuse.
        let hits: Vec<RetrievalHit> = raw
            .into_iter()
            .enumerate()
            .map(|(i, text)| RetrievalHit {
                chunk_id: format!("chunk-{i}"),
                text,
                file_path: None,
                start_line: None,
                end_line: None,
                score: 1.0,
                rank: i,
                tier: None,
            })
            .collect();

        // 4. Optimizer pipeline: dedup → window.
        let optimizer = ContextOptimizer::new(effective_budget);
        let result = optimizer.optimize(hits, &[]);

        // 5. Extract text from the selected hits.
        let chunks: Vec<String> = result.chunks.into_iter().map(|h| h.text).collect();

        AssembledContext {
            tokens_used: result.tokens_used,
            role: req.role.clone(),
            tier: tier.to_string(),
            chunks,
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Returns `true` when the model string identifies a small/cheap model.
///
/// Heuristic covers: claude-haiku-*, gemini-*-flash-lite, llama-8b, phi-3-mini,
/// qwen-*-7b, and similar size-limited models.
fn is_small_model(model: &str) -> bool {
    let m = model.to_lowercase();
    m.contains("haiku")
        || m.contains("flash-lite")
        || m.contains("flash_lite")
        || m.contains("-8b")
        || m.contains("mini")
        || m.contains("-7b")
        || m.contains("small")
}

/// Built-in default profiles matching `data/context_profiles.toml`.
fn default_profiles() -> Vec<ContextProfile> {
    vec![
        ContextProfile {
            role: "chat".into(),
            token_budget: 500,
            k_chunks: 3,
            tier: "minimal".into(),
        },
        ContextProfile {
            role: "build".into(),
            token_budget: 4000,
            k_chunks: 10,
            tier: "expanded".into(),
        },
        ContextProfile {
            role: "review".into(),
            token_budget: 6000,
            k_chunks: 15,
            tier: "full".into(),
        },
        ContextProfile {
            role: "research".into(),
            token_budget: 8000,
            k_chunks: 20,
            tier: "full".into(),
        },
        ContextProfile {
            role: "default".into(),
            token_budget: 4096,
            k_chunks: 10,
            tier: "standard".into(),
        },
    ]
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn make_chunks(n: usize, words_each: usize) -> Vec<String> {
        (0..n)
            .map(|i| format!("chunk-{i} {}", "word ".repeat(words_each)))
            .collect()
    }

    // ── Budget tests ──────────────────────────────────────────────────────────

    /// Small-window model (haiku) must receive a smaller slice than a large model
    /// when given the same raw chunks and the same role.
    #[test]
    fn small_model_gets_smaller_slice_than_large() {
        let assembler = ContextAssembler::default();

        // Use 50-word chunks; estimate_tokens ≈ ceil(50*1.33) = 67 tokens each.
        // Build role budget = 4000 tokens → many chunks.
        // Haiku → halved to 2000 → fewer chunks.
        let raw = make_chunks(100, 50);

        let req_large = ContextRequest {
            query: "test".into(),
            role: "build".into(),
            model: "claude-sonnet-4-6".into(),
            token_budget: 4000,
        };
        let req_small = ContextRequest {
            query: "test".into(),
            role: "build".into(),
            model: "claude-haiku-4-5".into(),
            token_budget: 4000,
        };

        let ctx_large = assembler.assemble(&req_large, raw.clone());
        let ctx_small = assembler.assemble(&req_small, raw.clone());

        assert!(
            ctx_small.tokens_used <= ctx_large.tokens_used,
            "small model must use ≤ tokens of large model: small={} large={}",
            ctx_small.tokens_used,
            ctx_large.tokens_used
        );
        assert!(
            ctx_small.chunks.len() <= ctx_large.chunks.len(),
            "small model must return ≤ chunks: small={} large={}",
            ctx_small.chunks.len(),
            ctx_large.chunks.len()
        );
    }

    /// Chat role must always assemble ≤500 tokens regardless of input size.
    #[test]
    fn chat_role_assembles_at_most_500_tokens() {
        let assembler = ContextAssembler::default();
        // Large chunks to stress-test the cap.
        let raw = make_chunks(50, 100); // 50 chunks × ~100 words ≈ 6650 tokens total

        let req = ContextRequest {
            query: "what is the context?".into(),
            role: "chat".into(),
            model: "claude-sonnet-4-6".into(),
            token_budget: 32768, // intentionally huge — role cap must win
        };

        let ctx = assembler.assemble(&req, raw);
        assert!(
            ctx.tokens_used <= 500,
            "chat role must be ≤500 tokens, got {}",
            ctx.tokens_used
        );
        assert_eq!(ctx.role, "chat");
        assert_eq!(ctx.tier, "minimal");
    }

    /// Empty input must produce zero chunks without panicking.
    #[test]
    fn empty_input_produces_empty_context() {
        let assembler = ContextAssembler::default();
        let req = ContextRequest {
            query: "test".into(),
            role: "build".into(),
            model: "claude-sonnet-4-6".into(),
            token_budget: 4096,
        };
        let ctx = assembler.assemble(&req, vec![]);
        assert!(ctx.chunks.is_empty());
        assert_eq!(ctx.tokens_used, 0);
    }

    // ── Profile loading ───────────────────────────────────────────────────────

    /// Default assembler must load ≥4 profiles (the built-in set has 5).
    #[test]
    fn default_assembler_has_at_least_4_profiles() {
        let assembler = ContextAssembler::default();
        assert!(
            assembler.profile_count() >= 4,
            "expected ≥4 built-in profiles, got {}",
            assembler.profile_count()
        );
    }

    /// `load_profiles` on a missing path must fall back to defaults (not error).
    #[test]
    fn load_profiles_missing_path_falls_back_to_defaults() {
        let assembler = ContextAssembler::load_profiles("/no/such/file.toml").unwrap();
        assert!(
            assembler.profile_count() >= 4,
            "fallback must have ≥4 profiles, got {}",
            assembler.profile_count()
        );
    }

    /// `load_profiles` on a valid TOML string must return ≥4 entries.
    #[test]
    fn load_profiles_from_toml_returns_at_least_4() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("profiles.toml");

        let content = r#"
[[profiles]]
role = "chat"
token_budget = 500
k_chunks = 3
tier = "minimal"

[[profiles]]
role = "build"
token_budget = 4000
k_chunks = 10
tier = "expanded"

[[profiles]]
role = "review"
token_budget = 6000
k_chunks = 15
tier = "full"

[[profiles]]
role = "research"
token_budget = 8000
k_chunks = 20
tier = "full"
"#;
        std::fs::write(&path, content).unwrap();
        let assembler = ContextAssembler::load_profiles(path.to_str().unwrap()).unwrap();
        assert!(
            assembler.profile_count() >= 4,
            "loaded profiles must be ≥4, got {}",
            assembler.profile_count()
        );
    }

    // ── is_small_model ────────────────────────────────────────────────────────

    #[test]
    fn haiku_is_small_model() {
        assert!(is_small_model("claude-haiku-4-5"));
    }

    #[test]
    fn flash_lite_is_small_model() {
        assert!(is_small_model("gemini-2.0-flash-lite"));
    }

    #[test]
    fn sonnet_is_not_small_model() {
        assert!(!is_small_model("claude-sonnet-4-6"));
    }

    #[test]
    fn empty_model_is_not_small() {
        assert!(!is_small_model(""));
    }

    // ── Flash-lite budget test ────────────────────────────────────────────────

    #[test]
    fn flash_lite_build_role_budget_is_halved() {
        let assembler = ContextAssembler::default();
        // build profile = 4000 tokens; flash-lite → 2000; sonnet → 4000
        let raw = make_chunks(200, 30); // plenty of chunks

        let req_lite = ContextRequest {
            query: "q".into(),
            role: "build".into(),
            model: "gemini-2.0-flash-lite".into(),
            token_budget: 4000,
        };
        let req_full = ContextRequest {
            query: "q".into(),
            role: "build".into(),
            model: "claude-sonnet-4-6".into(),
            token_budget: 4000,
        };

        let ctx_lite = assembler.assemble(&req_lite, raw.clone());
        let ctx_full = assembler.assemble(&req_full, raw.clone());

        // flash-lite must use no more than half of what sonnet uses
        assert!(
            ctx_lite.tokens_used * 2 <= ctx_full.tokens_used + 200, // +200 slack for rounding
            "flash-lite tokens_used={} should be ~half of sonnet={}",
            ctx_lite.tokens_used,
            ctx_full.tokens_used
        );
    }
}
