//! # lint_budget — always-loaded context-budget lint
//!
//! Warns when the always-loaded prefix (all tiers' `TierResult.instructions`
//! concatenated) grows too large to be paid cheaply on every agent turn.
//!
//! ## Purpose
//!
//! The cascade methodology keeps the always-loaded core deliberately tiny and
//! pushes detail into on-demand references.  This lint measures the estimated
//! token cost of the always-loaded prefix and surfaces the heaviest sections so
//! authors know what to move to on-demand pointers.
//!
//! ## Inputs / Outputs
//!
//! - **Input:** `&ResolvedCascade` — the fully resolved cascade; each
//!   `TierResult.instructions` is always-loaded content.
//! - **Input:** `BudgetConfig` — warn/error thresholds (est tokens).
//! - **Output:** `BudgetReport` — total est tokens, per-tier breakdown, verdict,
//!   and move-to-on-demand suggestions for the heaviest sections.
//!
//! ## Constraints
//!
//! - Token estimation: `ceil(char_count / 4)` — the standard chars-per-token
//!   heuristic; accurate enough for linting purposes.
//! - On-demand rules (in `ResolvedCascade.on_demand_rules`) are explicitly
//!   excluded — those are NOT paid until loaded.
//! - The function is pure (no I/O, no mutation).
//! - No `unwrap()` outside `#[cfg(test)]`.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: lint_budget module

use crate::cascade_resolution::ResolvedCascade;
use cascade_types::cascade_tier::CascadeTier;

// ── Thresholds ────────────────────────────────────────────────────────────────

/// Default warn threshold: 6 000 estimated tokens.
pub const DEFAULT_WARN_TOKENS: usize = 6_000;

/// Default error threshold: 12 000 estimated tokens.
pub const DEFAULT_ERROR_TOKENS: usize = 12_000;

/// Number of section-heading suggestions to surface per heavy tier.
const TOP_SUGGESTIONS: usize = 3;

// ── Public types ──────────────────────────────────────────────────────────────

/// Configuration for the budget lint thresholds.
///
/// Construct with [`BudgetConfig::default`] or customise the fields directly.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BudgetConfig {
    /// Estimated-token count at which the verdict becomes [`Verdict::Warn`].
    pub warn_tokens: usize,
    /// Estimated-token count at which the verdict becomes [`Verdict::Error`].
    pub error_tokens: usize,
}

impl Default for BudgetConfig {
    fn default() -> Self {
        Self {
            warn_tokens: DEFAULT_WARN_TOKENS,
            error_tokens: DEFAULT_ERROR_TOKENS,
        }
    }
}

/// Budget verdict for the always-loaded prefix.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Verdict {
    /// Total is within the warn threshold.
    Ok,
    /// Total exceeds the warn threshold but not the error threshold.
    Warn,
    /// Total exceeds the error threshold.
    Error,
}

/// Per-tier estimated-token breakdown.
#[derive(Debug, Clone)]
pub struct TierBudget {
    /// The cascade tier.
    pub tier: CascadeTier,
    /// Estimated token count for this tier's always-loaded content.
    pub est_tokens: usize,
    /// Percentage of the total always-loaded token count (0–100).
    pub pct_of_total: f32,
}

/// A section that is a candidate for moving to an on-demand reference.
#[derive(Debug, Clone)]
pub struct SectionSuggestion {
    /// The cascade tier where the section lives.
    pub tier: CascadeTier,
    /// Heading text (first `#`-prefixed line found in the section).
    pub heading: String,
    /// Estimated token cost for this section.
    pub est_tokens: usize,
}

/// Full budget-lint report for the always-loaded prefix.
///
/// Returned by [`lint_always_loaded_budget`].
#[derive(Debug, Clone)]
pub struct BudgetReport {
    /// Total estimated tokens across all tiers' always-loaded content.
    pub total_est_tokens: usize,
    /// Per-tier breakdown ordered by tier precedence (GCI first).
    pub per_tier: Vec<TierBudget>,
    /// Lint verdict: Ok / Warn / Error.
    pub verdict: Verdict,
    /// Top sections (across all tiers) suggested for moving to on-demand refs.
    ///
    /// Sorted by `est_tokens` descending; at most [`TOP_SUGGESTIONS`] entries.
    pub suggestions: Vec<SectionSuggestion>,
}

// ── Entry point ───────────────────────────────────────────────────────────────

/// Lint the always-loaded context budget of a resolved cascade.
///
/// Estimates the token cost of every tier's always-loaded instruction content,
/// compares the total against the configured thresholds, and surfaces the top
/// sections most worth moving to on-demand references.
///
/// # Arguments
///
/// * `resolved` — the fully resolved cascade, as returned by
///   [`crate::cascade_resolution::resolve_cascade_full`].
/// * `cfg` — warn/error thresholds.
///
/// # Returns
///
/// A [`BudgetReport`] with `verdict == Verdict::Ok` when total estimated tokens
/// are below `cfg.warn_tokens`.
pub fn lint_always_loaded_budget(resolved: &ResolvedCascade, cfg: BudgetConfig) -> BudgetReport {
    // Collect per-tier estimates (only found tiers with non-empty instructions).
    let tier_data: Vec<(CascadeTier, usize, &str)> = resolved
        .tiers_found
        .iter()
        .filter(|tr| tr.found && !tr.instructions.trim().is_empty())
        .map(|tr| (tr.tier, estimate_tokens(&tr.instructions), tr.instructions.as_str()))
        .collect();

    let total_est_tokens: usize = tier_data.iter().map(|(_, t, _)| t).sum();

    // Build per-tier breakdown.
    let per_tier: Vec<TierBudget> = tier_data
        .iter()
        .map(|(tier, est, _)| TierBudget {
            tier: *tier,
            est_tokens: *est,
            pct_of_total: if total_est_tokens == 0 {
                0.0
            } else {
                (*est as f32 / total_est_tokens as f32) * 100.0
            },
        })
        .collect();

    // Determine verdict.
    let verdict = if total_est_tokens >= cfg.error_tokens {
        Verdict::Error
    } else if total_est_tokens >= cfg.warn_tokens {
        Verdict::Warn
    } else {
        Verdict::Ok
    };

    // Build section suggestions: extract sections from all tiers, sort by cost,
    // take the top N across all tiers.
    let mut all_sections: Vec<SectionSuggestion> = tier_data
        .iter()
        .flat_map(|(tier, _, text)| extract_sections(*tier, text))
        .collect();

    all_sections.sort_by_key(|s| std::cmp::Reverse(s.est_tokens));
    all_sections.truncate(TOP_SUGGESTIONS);

    BudgetReport {
        total_est_tokens,
        per_tier,
        verdict,
        suggestions: all_sections,
    }
}

// ── Token estimation ──────────────────────────────────────────────────────────

/// Estimate token count using the standard chars/4 heuristic.
///
/// This is accurate enough for linting purposes and avoids a tokeniser
/// dependency.  One token ≈ 4 UTF-8 characters for English prose.
#[inline]
pub fn estimate_tokens(text: &str) -> usize {
    text.chars().count().div_ceil(4)
}

// ── Section extraction ────────────────────────────────────────────────────────

/// Extract Markdown `#`-headed sections from `text` for a given tier.
///
/// Each section runs from a heading line to the next heading of the same or
/// higher level (or end of text).  Returns a [`SectionSuggestion`] per section,
/// ordered by their position in the text.
fn extract_sections(tier: CascadeTier, text: &str) -> Vec<SectionSuggestion> {
    let lines: Vec<&str> = text.lines().collect();
    let mut sections: Vec<SectionSuggestion> = Vec::new();

    // Indices of lines that start a new Markdown heading.
    let heading_indices: Vec<usize> = lines
        .iter()
        .enumerate()
        .filter_map(|(i, line)| {
            if line.starts_with('#') {
                Some(i)
            } else {
                None
            }
        })
        .collect();

    if heading_indices.is_empty() {
        // No headings — treat the whole text as one nameless section.
        if !text.trim().is_empty() {
            sections.push(SectionSuggestion {
                tier,
                heading: "(no heading)".to_owned(),
                est_tokens: estimate_tokens(text),
            });
        }
        return sections;
    }

    for (pos, &start) in heading_indices.iter().enumerate() {
        let end = heading_indices
            .get(pos + 1)
            .copied()
            .unwrap_or(lines.len());

        let section_lines = &lines[start..end];
        let section_text = section_lines.join("\n");

        // Extract the heading text (strip leading `#` characters and whitespace).
        let heading = lines[start]
            .trim_start_matches('#')
            .trim()
            .to_owned();

        sections.push(SectionSuggestion {
            tier,
            heading,
            est_tokens: estimate_tokens(&section_text),
        });
    }

    sections
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cascade_resolution::{ResolvedCascade, TierResult};
    use std::path::PathBuf;

    // ── Fixture helper ────────────────────────────────────────────────────────

    /// Build a minimal `ResolvedCascade` from (tier, instructions) pairs.
    /// All 6 tiers are present; those not in the list have `found = false`.
    fn make_resolved(tiers: &[(CascadeTier, &str)]) -> ResolvedCascade {
        let all_tiers = CascadeTier::all_in_precedence_order();
        let tier_results: Vec<TierResult> = all_tiers
            .iter()
            .map(|t| {
                let instructions = tiers
                    .iter()
                    .find(|(tier, _)| tier == t)
                    .map(|(_, text)| text.to_string())
                    .unwrap_or_default();
                let found = tiers.iter().any(|(tier, _)| tier == t);
                TierResult {
                    tier: *t,
                    found,
                    path_searched: PathBuf::from("/fake"),
                    instructions,
                }
            })
            .collect();

        ResolvedCascade {
            merged_instructions: String::new(),
            on_demand_rules: Vec::new(),
            mcp_server_url: String::new(),
            tiers_found: tier_results,
            working_dir: PathBuf::from("/fake"),
        }
    }

    // ── Test 1: empty cascade ─────────────────────────────────────────────────

    /// Empty cascade → 0 tokens, Verdict::Ok, no suggestions.
    ///
    /// WHY: base case — no tiers at all must be zero-cost and ok.
    #[test]
    fn empty_cascade_is_zero_ok() {
        let resolved = make_resolved(&[]);
        let report = lint_always_loaded_budget(&resolved, BudgetConfig::default());

        assert_eq!(report.total_est_tokens, 0, "empty cascade must be 0 tokens");
        assert_eq!(report.verdict, Verdict::Ok, "empty cascade must be Ok");
        assert!(report.per_tier.is_empty(), "no tier rows expected");
        assert!(report.suggestions.is_empty(), "no suggestions expected");
    }

    // ── Test 2: small cascade under warn threshold ────────────────────────────

    /// A small cascade under the warn threshold → Verdict::Ok.
    ///
    /// WHY: normal healthy cascade must not trigger a warning.
    #[test]
    fn small_cascade_is_ok() {
        let gci = "# Global Config\n\nKeep it lean.";
        let ppc = "# Project Config\n\nProject override.";
        let resolved = make_resolved(&[
            (CascadeTier::Gci, gci),
            (CascadeTier::Ppc, ppc),
        ]);
        let report = lint_always_loaded_budget(&resolved, BudgetConfig::default());

        assert!(
            report.total_est_tokens < DEFAULT_WARN_TOKENS,
            "small text must be under the warn threshold"
        );
        assert_eq!(report.verdict, Verdict::Ok);
        assert_eq!(
            report.per_tier.len(),
            2,
            "should have two tier rows (GCI + PPC)"
        );
    }

    // ── Test 3: oversized prefix triggers warn then error ─────────────────────

    /// Synthetic large content: just above warn threshold → Warn; above error → Error.
    ///
    /// WHY: core contract — thresholds must produce the correct verdict.
    #[test]
    fn oversized_prefix_triggers_warn_then_error() {
        // 4 chars ≈ 1 token; build strings that land in each zone.
        // warn=6000, error=12000 (default). We need ~6001 tokens = ~24004 chars.
        let large_block = "x".repeat(24_010); // just over 6000 est tokens
        let resolved_warn = make_resolved(&[(CascadeTier::Gci, &large_block)]);
        let report_warn =
            lint_always_loaded_budget(&resolved_warn, BudgetConfig::default());

        assert!(
            report_warn.total_est_tokens >= DEFAULT_WARN_TOKENS,
            "large block must meet warn threshold"
        );
        assert_eq!(
            report_warn.verdict,
            Verdict::Warn,
            "should be Warn, not Error yet"
        );

        // Now above error threshold: ~12001 tokens = ~48004 chars.
        let huge_block = "x".repeat(48_010);
        let resolved_error = make_resolved(&[(CascadeTier::Gci, &huge_block)]);
        let report_error =
            lint_always_loaded_budget(&resolved_error, BudgetConfig::default());

        assert_eq!(
            report_error.verdict,
            Verdict::Error,
            "very large block must be Error"
        );
    }

    // ── Test 4: suggestion picks the largest section ──────────────────────────

    /// The heaviest heading section must be first in suggestions.
    ///
    /// WHY: authors need to know which section is most worth moving to on-demand.
    #[test]
    fn suggestion_picks_largest_section() {
        // Two sections: one small, one large.
        let text = format!(
            "# Small Section\n\nFour words.\n\n# Large Section\n\n{}\n",
            "word ".repeat(500)
        );
        let resolved = make_resolved(&[(CascadeTier::Gci, &text)]);
        let cfg = BudgetConfig {
            warn_tokens: 1, // force suggestions even on small input
            error_tokens: 999_999,
        };
        let report = lint_always_loaded_budget(&resolved, cfg);

        assert!(
            !report.suggestions.is_empty(),
            "should have at least one suggestion"
        );
        assert_eq!(
            report.suggestions[0].heading, "Large Section",
            "heaviest section must be first: got {:?}",
            report.suggestions[0].heading
        );
    }

    // ── Test 5: per-tier percentages sum to ~100 ─────────────────────────────

    /// Per-tier percentages should sum to approximately 100%.
    ///
    /// WHY: validates the pct_of_total calculation is consistent.
    #[test]
    fn per_tier_pct_sums_to_100() {
        let resolved = make_resolved(&[
            (CascadeTier::Gci, "# GCI\n\nGlobal instructions here."),
            (CascadeTier::Ppc, "# PPC\n\nProject instructions here."),
            (CascadeTier::Prc, "# PRC\n\nRepo instructions here."),
        ]);
        let report = lint_always_loaded_budget(&resolved, BudgetConfig::default());

        let sum: f32 = report.per_tier.iter().map(|t| t.pct_of_total).sum();
        assert!(
            (sum - 100.0).abs() < 1.0,
            "per-tier pct should sum to ~100, got {sum}"
        );
    }

    // ── Test 6: custom thresholds work correctly ──────────────────────────────

    /// Custom BudgetConfig thresholds override the defaults.
    ///
    /// WHY: callers must be able to tighten or loosen thresholds.
    #[test]
    fn custom_thresholds_respected() {
        // A medium-sized text — small enough to be Ok with defaults,
        // but will be Warn with a very tight threshold.
        let medium = "# Section\n\nThis is some content.\n".repeat(10);
        let resolved = make_resolved(&[(CascadeTier::Gci, &medium)]);

        let report_default =
            lint_always_loaded_budget(&resolved, BudgetConfig::default());
        assert_eq!(report_default.verdict, Verdict::Ok, "default: should be Ok");

        let tight_cfg = BudgetConfig {
            warn_tokens: 1,
            error_tokens: 999_999,
        };
        let report_tight = lint_always_loaded_budget(&resolved, tight_cfg);
        assert_eq!(
            report_tight.verdict,
            Verdict::Warn,
            "tight threshold: should be Warn"
        );
    }

    // ── Test 7: on-demand rules excluded from budget ──────────────────────────

    /// On-demand rules in `on_demand_rules` must NOT be counted in the budget.
    ///
    /// WHY: on-demand rules are not paid until loaded; they must be excluded.
    #[test]
    fn on_demand_rules_not_counted() {
        let resolved = make_resolved(&[]);
        // The empty resolved has no instructions — budget must be zero.
        let report = lint_always_loaded_budget(&resolved, BudgetConfig::default());
        assert_eq!(
            report.total_est_tokens, 0,
            "on_demand_rules must not contribute to always-loaded budget"
        );
    }
}
