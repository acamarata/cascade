//! # lint_duplication — cross-tier "reference up, never duplicate" lint
//!
//! Detects when a lower (more-specific) cascade tier copies a higher tier's
//! instruction text verbatim instead of referencing it.  Authors get a
//! structured finding list they can display in `cascade doctor` or surface as
//! warnings in CI.
//!
//! ## Purpose
//!
//! The cascade instruction hierarchy (GCI → PCI → APC → PPC → PRC → PAC)
//! demands that lower tiers reference higher tiers rather than repeating
//! their content.  This module provides a pure, deterministic function that
//! compares every pair (lower tier, higher tier) and flags verbatim matches.
//!
//! ## Inputs / Outputs
//!
//! - **Input:** `&ResolvedCascade` — the fully resolved cascade containing
//!   each tier's instruction text via `tiers_found`.
//! - **Output:** `Vec<DuplicationFinding>` — one entry per duplicated block;
//!   empty when no duplication is found.
//!
//! ## Constraints
//!
//! - Blocks shorter than [`MIN_BLOCK_LEN`] characters **or** with fewer than
//!   [`MIN_BLOCK_LINES`] non-empty lines are ignored to avoid flagging common
//!   short headers like `# Global Instructions`.
//! - Whitespace is normalised before comparison (consecutive whitespace
//!   collapsed to a single space, leading/trailing stripped per line) so
//!   minor formatting differences do not count as different content.
//! - The function is pure (no I/O, no mutation).
//! - No `unwrap()` outside of `#[cfg(test)]` blocks.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: lint_duplication module (P12)

use crate::cascade_resolution::ResolvedCascade;
use cascade_types::cascade_tier::CascadeTier;

// ── Thresholds ────────────────────────────────────────────────────────────────

/// Minimum character length (after normalisation) for a block to be eligible
/// for duplication checking.  Blocks shorter than this are too small to carry
/// unique content — common headers (`# Section`, `## Subsection`) would
/// generate false positives if we checked them.
const MIN_BLOCK_LEN: usize = 120;

/// Minimum number of non-empty lines for a block to be eligible.  Guards
/// against single-line paragraphs that coincidentally match.
const MIN_BLOCK_LINES: usize = 3;

// ── Public types ──────────────────────────────────────────────────────────────

/// A single duplication finding: a lower tier copied a higher tier's content.
///
/// Carry the tier names and a short snippet (first non-empty line of the
/// duplicated block, truncated to 80 chars) so callers can display a useful
/// message without loading full instruction texts.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DuplicationFinding {
    /// The lower (more-specific) tier that contains the duplicate content.
    pub lower_tier: CascadeTier,
    /// The higher (less-specific) tier that originally defined the content.
    pub higher_tier: CascadeTier,
    /// First non-empty line of the duplicated block, truncated to 80 chars.
    /// Prefixed with `'...'` when truncated.
    pub snippet: String,
}

// ── Entry point ───────────────────────────────────────────────────────────────

/// Scan a resolved cascade for cross-tier content duplication.
///
/// For every found tier, compare its content blocks against every HIGHER
/// (more-authoritative) tier.  Return a [`DuplicationFinding`] for each
/// non-trivial block that appears verbatim in both.
///
/// # Arguments
///
/// * `resolved` — the fully resolved cascade, as returned by
///   [`crate::cascade_resolution::resolve_cascade_full`].
///
/// # Returns
///
/// `Vec<DuplicationFinding>` — empty when no duplication is detected.
///
/// # Algorithm
///
/// 1. Split each tier's instruction text into paragraph-level blocks
///    (double-newline delimiters).
/// 2. Normalise each block (collapse internal whitespace; trim lines).
/// 3. Discard blocks below the size thresholds.
/// 4. For every (lower, higher) tier pair (in precedence order) check
///    whether any of the lower tier's blocks appear in the higher tier's
///    normalised text.
/// 5. Emit one finding per matching block (deduplicated by block text so
///    a block that matches multiple higher tiers generates one finding per
///    higher tier — the most-authoritative one is typically most useful).
pub fn lint_duplication(resolved: &ResolvedCascade) -> Vec<DuplicationFinding> {
    // `tiers_found` is already in precedence order: GCI first (index 0), PAC last.
    // We only work on tiers that were actually found and have non-empty content.
    let found: Vec<&crate::cascade_resolution::TierResult> = resolved
        .tiers_found
        .iter()
        .filter(|t| t.found && !t.instructions.trim().is_empty())
        .collect();

    let mut findings = Vec::new();

    // For each found tier (by index), compare against all higher-precedence
    // tiers (lower index = higher authority in the precedence ordering).
    for lower_idx in 1..found.len() {
        let lower = found[lower_idx];
        let lower_blocks = eligible_blocks(&lower.instructions);

        for &higher in &found[..lower_idx] {
            let higher_normalised = normalise_text(&higher.instructions);

            for block in &lower_blocks {
                if higher_normalised.contains(block.normalised.as_str()) {
                    findings.push(DuplicationFinding {
                        lower_tier: lower.tier,
                        higher_tier: higher.tier,
                        snippet: block.snippet.clone(),
                    });
                }
            }
        }
    }

    findings
}

// ── Normalisation helpers ─────────────────────────────────────────────────────

/// An eligible block extracted from a tier's instruction text.
struct Block {
    /// Normalised text used for comparison.
    normalised: String,
    /// Short display snippet (first non-empty line, ≤80 chars).
    snippet: String,
}

/// Split `text` into paragraph blocks and return only those that meet the
/// size thresholds.
///
/// A "paragraph" is a sequence of lines separated by one or more blank lines.
/// Each paragraph is individually normalised before threshold checks.
fn eligible_blocks(text: &str) -> Vec<Block> {
    split_paragraphs(text)
        .into_iter()
        .filter_map(|para| {
            let normalised = normalise_text(&para);
            let non_empty_lines = para.lines().filter(|l| !l.trim().is_empty()).count();

            if normalised.len() >= MIN_BLOCK_LEN && non_empty_lines >= MIN_BLOCK_LINES {
                let snippet = make_snippet(&para);
                Some(Block { normalised, snippet })
            } else {
                None
            }
        })
        .collect()
}

/// Split `text` on blank-line boundaries into non-empty paragraph strings.
fn split_paragraphs(text: &str) -> Vec<String> {
    let mut paragraphs = Vec::new();
    let mut current = Vec::<&str>::new();

    for line in text.lines() {
        if line.trim().is_empty() {
            if !current.is_empty() {
                paragraphs.push(current.join("\n"));
                current.clear();
            }
        } else {
            current.push(line);
        }
    }
    if !current.is_empty() {
        paragraphs.push(current.join("\n"));
    }
    paragraphs
}

/// Normalise a text block for comparison:
/// 1. Trim each line.
/// 2. Collapse consecutive whitespace within each line to a single space.
/// 3. Join lines with a single newline.
/// 4. Trim the whole result.
fn normalise_text(text: &str) -> String {
    text.lines()
        .map(|l| collapse_whitespace(l.trim()))
        .collect::<Vec<_>>()
        .join("\n")
        .trim()
        .to_owned()
}

/// Collapse any run of whitespace characters to a single ASCII space.
fn collapse_whitespace(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut last_was_space = false;
    for ch in s.chars() {
        if ch.is_whitespace() {
            if !last_was_space {
                out.push(' ');
            }
            last_was_space = true;
        } else {
            out.push(ch);
            last_was_space = false;
        }
    }
    out
}

/// Extract a display snippet: first non-empty line of `para`, truncated to
/// 80 characters.  Appends `'...'` when truncated.
fn make_snippet(para: &str) -> String {
    let first = para
        .lines()
        .find(|l| !l.trim().is_empty())
        .unwrap_or("")
        .trim();

    if first.len() <= 80 {
        first.to_owned()
    } else {
        format!("{}...", &first[..77])
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cascade_resolution::TierResult;
    use std::path::PathBuf;

    /// Build a minimal `ResolvedCascade` from a list of (tier, instructions) pairs.
    /// Tiers are given in precedence order (GCI first).
    fn make_resolved(tiers: &[(CascadeTier, &str)]) -> ResolvedCascade {
        let tier_results: Vec<TierResult> = tiers
            .iter()
            .map(|(tier, instructions)| TierResult {
                tier: *tier,
                found: !instructions.is_empty(),
                path_searched: PathBuf::from("/fake"),
                instructions: instructions.to_string(),
            })
            .collect();

        // Fill in the remaining 6 tiers as missing
        let all_tiers = cascade_types::cascade_tier::CascadeTier::all_in_precedence_order();
        let mut all_results: Vec<TierResult> = all_tiers
            .iter()
            .map(|t| {
                tier_results
                    .iter()
                    .find(|tr| tr.tier == *t)
                    .cloned()
                    .unwrap_or(TierResult {
                        tier: *t,
                        found: false,
                        path_searched: PathBuf::from("/fake"),
                        instructions: String::new(),
                    })
            })
            .collect();

        // Mark explicitly listed tiers as found
        for tr in &mut all_results {
            if tiers.iter().any(|(t, _)| *t == tr.tier) {
                tr.found = true;
            }
        }

        ResolvedCascade {
            merged_instructions: String::new(),
            on_demand_rules: Vec::new(),
            vars: Default::default(),
            mcp_server_url: String::new(),
            tiers_found: all_results,
            working_dir: PathBuf::from("/fake"),
        }
    }

    // ── Test 1: duplication detected ─────────────────────────────────────────

    /// Lower tier (PPC) verbatim-copies a higher tier (GCI) block → finding produced.
    ///
    /// WHY: core contract — when a non-trivial block is duplicated downward,
    /// one DuplicationFinding must be emitted for each such block.
    #[test]
    fn detects_verbatim_duplication() {
        // A block that is large enough to exceed both thresholds.
        let shared_block = "This is a detailed instruction block that explains \
            how to configure the cascade tier settings for all projects.\n\
            It covers the correct procedure for referencing higher tiers,\n\
            and must never be duplicated verbatim into a lower tier file.\n\
            Authors should reference the higher tier instead of copy-pasting.";

        let gci_text = format!("# Global Config\n\n{shared_block}\n\nSome other GCI content.");
        let ppc_text = format!("# Project Config\n\n{shared_block}\n\nProject-specific note.");

        let resolved = make_resolved(&[
            (CascadeTier::Gci, &gci_text),
            (CascadeTier::Ppc, &ppc_text),
        ]);

        let findings = lint_duplication(&resolved);
        assert!(
            !findings.is_empty(),
            "must detect duplication when lower tier copies higher tier block"
        );
        assert_eq!(findings[0].lower_tier, CascadeTier::Ppc);
        assert_eq!(findings[0].higher_tier, CascadeTier::Gci);
        assert!(
            findings[0].snippet.contains("This is a detailed"),
            "snippet must contain the first line: {:?}",
            findings[0].snippet
        );
    }

    // ── Test 2: no duplication → no findings ─────────────────────────────────

    /// Different content in each tier → no findings produced.
    ///
    /// WHY: must not generate false positives when tiers contain distinct text.
    #[test]
    fn no_findings_when_content_is_distinct() {
        let gci_text = "# Global\n\nThis GCI block describes global policy for all projects,\n\
            covering authentication, logging, and audit requirements.\n\
            It is unique to the GCI tier and is never repeated below.";
        let ppc_text = "# Project\n\nThis PPC block describes project-specific configuration,\n\
            covering project-level overrides and team conventions.\n\
            It references the GCI via pointer rather than duplicating content.";

        let resolved = make_resolved(&[
            (CascadeTier::Gci, gci_text),
            (CascadeTier::Ppc, ppc_text),
        ]);

        let findings = lint_duplication(&resolved);
        assert!(
            findings.is_empty(),
            "no findings expected for distinct content: {findings:#?}"
        );
    }

    // ── Test 3: short shared headers are NOT flagged ──────────────────────────

    /// A short line (< MIN_BLOCK_LEN chars) shared across tiers must NOT trigger
    /// a finding — it falls below the minimum block size threshold.
    ///
    /// WHY: common headings like "# Global Instructions" or "## Setup" appear in
    /// multiple tiers and must not generate false positives.
    #[test]
    fn short_shared_header_not_flagged() {
        // Only one line, definitely < 120 chars and < 3 non-empty lines.
        let header = "# Global Instructions";
        let gci_text = format!("{header}\n\nUnique GCI policy that is not shared.");
        let ppc_text = format!("{header}\n\nUnique PPC policy that is not shared.");

        let resolved = make_resolved(&[
            (CascadeTier::Gci, &gci_text),
            (CascadeTier::Ppc, &ppc_text),
        ]);

        let findings = lint_duplication(&resolved);
        assert!(
            findings.is_empty(),
            "short shared header must not be flagged: {findings:#?}"
        );
    }

    // ── Test 4: whitespace normalisation ─────────────────────────────────────

    /// A block with differing whitespace (extra spaces, tabs) must still match
    /// when the content is otherwise identical.
    ///
    /// WHY: authors may reformat without changing content; the lint must catch
    /// the semantic duplication regardless of whitespace style.
    #[test]
    fn normalisation_catches_whitespace_variants() {
        let canonical = "This is a long policy block shared across tiers.\n\
            It covers multiple important topics in detail.\n\
            Always reference higher tiers rather than duplicating content here.";

        // Same content but with extra internal spaces and trailing whitespace.
        let reformatted = "This  is  a  long  policy  block  shared  across  tiers.  \n\
            It  covers  multiple  important  topics  in  detail.  \n\
            Always  reference  higher  tiers  rather  than  duplicating  content  here.";

        let gci_text = format!("# GCI Header\n\n{canonical}\n\nGCI footer text only.");
        let ppc_text = format!("# PPC Header\n\n{reformatted}\n\nPPC footer text only.");

        let resolved = make_resolved(&[
            (CascadeTier::Gci, &gci_text),
            (CascadeTier::Ppc, &ppc_text),
        ]);

        let findings = lint_duplication(&resolved);
        assert!(
            !findings.is_empty(),
            "whitespace-normalised duplication must be detected"
        );
        assert_eq!(findings[0].lower_tier, CascadeTier::Ppc);
        assert_eq!(findings[0].higher_tier, CascadeTier::Gci);
    }

    // ── Test 5: only same-tier content, no cross-tier duplication ────────────

    /// When only one tier is present, there is nothing to compare — no findings.
    ///
    /// WHY: edge case; must not panic or produce spurious output.
    #[test]
    fn single_tier_produces_no_findings() {
        let gci_text = "# GCI\n\nGlobal policy block that is long enough to cross thresholds.\n\
            Line two of the policy block.\n\
            Line three of the policy block that pushes it over the minimum length requirement.";

        let resolved = make_resolved(&[(CascadeTier::Gci, gci_text)]);
        let findings = lint_duplication(&resolved);
        assert!(
            findings.is_empty(),
            "single tier cannot have cross-tier duplication"
        );
    }
}
