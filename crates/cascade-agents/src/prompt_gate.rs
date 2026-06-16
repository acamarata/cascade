//! Prompt-size gate — token estimation and spawn-time guard.
//!
//! Purpose: Estimate the token count of an assembled agent system prompt at
//!   spawn time, emit structured diagnostics when the estimate exceeds
//!   configurable warn/error thresholds, and optionally block spawning when
//!   the prompt is above the error threshold.
//!
//! Inputs: assembled system-prompt `&str` + `PromptSizeConfig`.
//!
//! Outputs:
//!   - `PromptSizeReport` carrying the estimate, threshold outcome, and the
//!     top-3 largest sections (for trimming guidance).
//!   - `tracing::warn` / `tracing::error` events emitted automatically by
//!     `check_prompt_size`.
//!
//! Constraints:
//!   - No heavy tokenizer dependency.  Uses the chars-÷-4 heuristic with a
//!     +10 % rounding-up safety margin (see `estimate_tokens` docs).
//!   - Sections are split on `\n\n` or markdown headings (`# …`).
//!   - All fields are `serde`-serialisable so the report can be forwarded to
//!     the telemetry pipeline.
//!
//! SPORT: cascade-agents / prompt_gate — E-P13-01

use std::cmp::Reverse;

use serde::{Deserialize, Serialize};
use tracing::{error, warn};

// ── Token estimation ──────────────────────────────────────────────────────────

/// Estimate the number of tokens in `text` without a full tokenizer.
///
/// # Method
/// 1. Count Unicode scalar values (`str::chars().count()`).
/// 2. Divide by 4 (the widely-used chars-per-token approximation for English /
///    code content, derived from OpenAI's rule-of-thumb).
/// 3. Apply a 10 % upward safety margin and round up, so the gate errs on
///    the side of caution rather than letting a prompt slip through.
///
/// # Accuracy
/// The estimate is intentionally cheap and conservative:
/// - English prose: typically within ±15 % of actual BPE token counts.
/// - Code / JSON heavy prompts: may over-count by up to 25 % (code tokens
///   tend to be longer, so chars÷4 is generous — safe direction for a gate).
/// - CJK / Arabic text: under-counts significantly; but the Cascade codebase
///   and agent prompts are English + code, so this is acceptable.
///
/// # Monotonicity
/// `estimate_tokens(a)` ≤ `estimate_tokens(a + b)` for any strings `a`, `b`
/// of non-negative length.
///
/// # Example
/// ```
/// use cascade_agents::prompt_gate::estimate_tokens;
/// let n = estimate_tokens("Hello, world! This is a test prompt.");
/// assert!(n > 0);
/// ```
pub fn estimate_tokens(text: &str) -> usize {
    let char_count = text.chars().count();
    // chars ÷ 4 (ceiling), then add 10 % safety margin, rounded up.
    // Use saturating arithmetic to avoid overflow on pathological inputs.
    let base = char_count.div_ceil(4); // ceiling division by 4
    base.saturating_add(base / 10) // +10 %
}

// ── Section extraction ────────────────────────────────────────────────────────

/// A labelled section extracted from a prompt.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct PromptSection {
    /// First line of the section (used as the label in diagnostics).
    pub label: String,
    /// Estimated tokens for this section alone.
    pub estimated_tokens: usize,
}

/// Split `text` into sections on double-newline or markdown heading boundaries,
/// then return the top-N largest by `estimated_tokens`, descending.
///
/// Used to pinpoint which parts of an oversized prompt are the largest so the
/// caller can trim them.
pub fn top_sections(text: &str, n: usize) -> Vec<PromptSection> {
    // Split on double-newline or lines that start with one or more `#` chars.
    // We use a simple character-scan rather than a regex to keep zero extra deps.
    let mut sections: Vec<PromptSection> = Vec::new();
    let mut current_start = 0usize;

    let bytes = text.as_bytes();
    let len = bytes.len();
    let mut i = 0usize;

    while i < len {
        // Check for double-newline boundary: \n\n
        let is_double_nl = i + 1 < len && bytes[i] == b'\n' && bytes[i + 1] == b'\n';
        // Check for markdown heading: line starts with '#'
        let is_heading = i == 0
            || (bytes[i] == b'\n'
                && i + 1 < len
                && bytes[i + 1] == b'#');

        if (is_double_nl || is_heading) && i > current_start {
            let slice = &text[current_start..i];
            if !slice.trim().is_empty() {
                sections.push(section_from_slice(slice));
            }
            // Skip past the boundary chars
            current_start = if is_double_nl { i + 2 } else { i + 1 };
        }
        i += 1;
    }

    // Trailing section
    if current_start < len {
        let slice = &text[current_start..];
        if !slice.trim().is_empty() {
            sections.push(section_from_slice(slice));
        }
    }

    sections.sort_by_key(|s| Reverse(s.estimated_tokens));
    sections.truncate(n);
    sections
}

fn section_from_slice(s: &str) -> PromptSection {
    // Use the first non-empty line as the label, truncated to 80 chars.
    let label = s
        .lines()
        .find(|l| !l.trim().is_empty())
        .unwrap_or("<section>")
        .chars()
        .take(80)
        .collect::<String>();

    PromptSection {
        label,
        estimated_tokens: estimate_tokens(s),
    }
}

// ── Configuration ─────────────────────────────────────────────────────────────

/// Configuration for the prompt-size gate.
///
/// Thresholds are in estimated tokens (see [`estimate_tokens`]).
///
/// # Defaults
/// - `warn`: 2 000 tokens — emit a `tracing::warn` with top-3 sections.
/// - `error`: 4 000 tokens — emit a `tracing::error` and, when
///   `block_on_error` is `true` (the default), return an error from
///   [`check_prompt_size`] to prevent spawning.
/// - `block_on_error`: `true` — hard-block spawning above the error threshold.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct PromptSizeConfig {
    /// Token estimate above which a warning is emitted.
    #[serde(default = "default_warn")]
    pub warn: usize,
    /// Token estimate above which an error is emitted (and spawn is blocked
    /// when `block_on_error` is `true`).
    #[serde(default = "default_error")]
    pub error: usize,
    /// When `true` (default), spawning is refused if the estimate exceeds
    /// `error`.  Set to `false` to log-only without blocking.
    #[serde(default = "default_block")]
    pub block_on_error: bool,
}

fn default_warn() -> usize {
    2_000
}
fn default_error() -> usize {
    4_000
}
fn default_block() -> bool {
    true
}

impl Default for PromptSizeConfig {
    fn default() -> Self {
        Self {
            warn: default_warn(),
            error: default_error(),
            block_on_error: default_block(),
        }
    }
}

// ── Report ────────────────────────────────────────────────────────────────────

/// The outcome of a single gate check.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase", tag = "level", content = "data")]
pub enum GateOutcome {
    /// Estimate is below the warn threshold — no action needed.
    Ok,
    /// Estimate exceeded the warn threshold; diagnostics are in the report.
    Warned,
    /// Estimate exceeded the error threshold.  When `block_on_error` is set
    /// this also causes [`check_prompt_size`] to return `Err`.
    Errored,
}

/// Full diagnostic report returned by [`check_prompt_size`].
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PromptSizeReport {
    /// Estimated token count for the whole prompt.
    pub estimated_tokens: usize,
    /// Gate outcome (Ok / Warned / Errored).
    pub outcome: GateOutcome,
    /// Top-3 largest sections by estimated token count (populated only when
    /// `outcome` is `Warned` or `Errored`).
    pub top_sections: Vec<PromptSection>,
}

// ── Gate error ────────────────────────────────────────────────────────────────

/// Error returned when the prompt size gate refuses to spawn an agent.
#[derive(Debug, thiserror::Error)]
#[error("system prompt too large ({estimated} estimated tokens, limit {limit}) — reduce the largest sections: {sections}")]
pub struct PromptTooLarge {
    pub estimated: usize,
    pub limit: usize,
    pub sections: String,
}

// ── Gate check ────────────────────────────────────────────────────────────────

/// Check `prompt` against `cfg` and return a `PromptSizeReport`.
///
/// Side-effects:
/// - Emits `tracing::warn` (with top-3 sections) when estimate > `cfg.warn`.
/// - Emits `tracing::error` when estimate > `cfg.error`.
/// - Returns `Err(PromptTooLarge)` when estimate > `cfg.error` **and**
///   `cfg.block_on_error` is `true`.
///
/// When the estimate is within bounds, returns `Ok(report)` with
/// `report.outcome == GateOutcome::Ok` and an empty `top_sections` vec.
pub fn check_prompt_size(
    prompt: &str,
    cfg: &PromptSizeConfig,
) -> Result<PromptSizeReport, PromptTooLarge> {
    let estimated = estimate_tokens(prompt);

    if estimated > cfg.error {
        let sections = top_sections(prompt, 3);
        let section_summary = sections
            .iter()
            .enumerate()
            .map(|(i, s)| format!("#{}: '{}' (~{} tok)", i + 1, s.label, s.estimated_tokens))
            .collect::<Vec<_>>()
            .join(", ");

        error!(
            estimated_tokens = estimated,
            error_threshold = cfg.error,
            top_sections = %section_summary,
            "agent system prompt exceeds error threshold — spawn blocked"
        );

        let report = PromptSizeReport {
            estimated_tokens: estimated,
            outcome: GateOutcome::Errored,
            top_sections: sections.clone(),
        };

        if cfg.block_on_error {
            return Err(PromptTooLarge {
                estimated,
                limit: cfg.error,
                sections: section_summary,
            });
        }

        return Ok(report);
    }

    if estimated > cfg.warn {
        let sections = top_sections(prompt, 3);
        let section_summary = sections
            .iter()
            .enumerate()
            .map(|(i, s)| format!("#{}: '{}' (~{} tok)", i + 1, s.label, s.estimated_tokens))
            .collect::<Vec<_>>()
            .join(", ");

        warn!(
            estimated_tokens = estimated,
            warn_threshold = cfg.warn,
            top_sections = %section_summary,
            "agent system prompt exceeds warn threshold"
        );

        return Ok(PromptSizeReport {
            estimated_tokens: estimated,
            outcome: GateOutcome::Warned,
            top_sections: sections,
        });
    }

    Ok(PromptSizeReport {
        estimated_tokens: estimated,
        outcome: GateOutcome::Ok,
        top_sections: vec![],
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── T1: estimate_tokens is monotonic ─────────────────────────────────────

    #[test]
    fn estimate_tokens_monotonic() {
        let base = "The quick brown fox.";
        let extended = "The quick brown fox. jumps over the lazy dog and keeps going.";
        assert!(estimate_tokens(extended) >= estimate_tokens(base));
    }

    // ── T2: estimate is roughly right (sanity: 4 chars ≈ 1 token) ────────────

    #[test]
    fn estimate_tokens_roughly_correct() {
        // 400 chars → ~100 base tokens → ~110 with 10% margin
        let text = "a".repeat(400);
        let est = estimate_tokens(&text);
        // Should be in [100, 130] — within 30% of naive expectation
        assert!(est >= 100, "estimate too low: {est}");
        assert!(est <= 130, "estimate too high: {est}");
    }

    // ── T3: empty string → 0 tokens ──────────────────────────────────────────

    #[test]
    fn estimate_tokens_empty() {
        assert_eq!(estimate_tokens(""), 0);
    }

    // ── T4: small prompt passes silently (Ok outcome, no sections) ───────────

    #[test]
    fn small_prompt_passes_silently() {
        let cfg = PromptSizeConfig::default();
        let prompt = "You are a short agent. Do task X.";
        let report = check_prompt_size(prompt, &cfg).unwrap();
        assert_eq!(report.outcome, GateOutcome::Ok);
        assert!(report.top_sections.is_empty());
        assert!(report.estimated_tokens < cfg.warn);
    }

    // ── T5: prompt above warn → Warned outcome with sections ─────────────────

    #[test]
    fn prompt_above_warn_returns_warned_with_sections() {
        let cfg = PromptSizeConfig {
            warn: 10,
            error: 10_000,
            block_on_error: true,
        };
        // Build a multi-section prompt clearly above warn=10 tokens
        let prompt = "# Section Alpha\n\n".to_string()
            + &"word ".repeat(200)
            + "\n\n# Section Beta\n\n"
            + &"word ".repeat(100);

        let report = check_prompt_size(&prompt, &cfg).unwrap();
        assert_eq!(report.outcome, GateOutcome::Warned);
        // Should report some top sections
        assert!(!report.top_sections.is_empty());
        assert!(report.estimated_tokens > cfg.warn);
    }

    // ── T6: prompt above error, block_on_error=true → Err ────────────────────

    #[test]
    fn prompt_above_error_blocked_when_flag_true() {
        let cfg = PromptSizeConfig {
            warn: 5,
            error: 20,
            block_on_error: true,
        };
        let prompt = "word ".repeat(500); // ~625 est tokens >> 20
        let result = check_prompt_size(&prompt, &cfg);
        assert!(
            result.is_err(),
            "expected Err from blocked oversized prompt"
        );
        let err = result.unwrap_err();
        assert!(err.estimated > cfg.error);
    }

    // ── T7: prompt above error, block_on_error=false → Ok(Errored) ───────────

    #[test]
    fn prompt_above_error_passes_when_flag_false() {
        let cfg = PromptSizeConfig {
            warn: 5,
            error: 20,
            block_on_error: false,
        };
        let prompt = "word ".repeat(500);
        let report = check_prompt_size(&prompt, &cfg).unwrap();
        assert_eq!(report.outcome, GateOutcome::Errored);
        assert!(report.estimated_tokens > cfg.error);
        assert!(!report.top_sections.is_empty());
    }

    // ── T8: top_sections returns at most N sections, sorted desc ─────────────

    #[test]
    fn top_sections_capped_and_sorted_descending() {
        let long = "word ".repeat(200);
        let medium = "word ".repeat(50);
        let short = "word ".repeat(10);
        let prompt = format!(
            "# Short\n\n{short}\n\n# Medium\n\n{medium}\n\n# Long\n\n{long}"
        );
        let sections = top_sections(&prompt, 3);
        // All three sections present and in descending order
        assert_eq!(sections.len(), 3);
        assert!(sections[0].estimated_tokens >= sections[1].estimated_tokens);
        assert!(sections[1].estimated_tokens >= sections[2].estimated_tokens);
        // The longest section should be first
        assert!(sections[0].estimated_tokens > sections[2].estimated_tokens);
    }

    // ── T9: top_sections N=2 caps correctly ──────────────────────────────────

    #[test]
    fn top_sections_truncated_to_n() {
        let prompt = "# A\n\naaaaa\n\n# B\n\nbbbbb\n\n# C\n\nccccc";
        let sections = top_sections(prompt, 2);
        assert!(sections.len() <= 2);
    }
}
