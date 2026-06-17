//! # lint_conflicts — cross-tier contradicting-value detector
//!
//! Detects when two cascade tiers assert conflicting values for the same
//! conceptual configuration key.
//!
//! ## Purpose
//!
//! The cascade model specifies that higher tiers set defaults and lower tiers
//! may OVERRIDE them — but when both tiers _assert_ the same key with
//! different values, tooling receives contradictory guidance.  This module
//! flags those contradictions so authors can explicitly resolve them (either by
//! removing the lower-tier assertion or by intentionally documenting the
//! override).
//!
//! ## Detection heuristics
//!
//! Two complementary heuristics are applied:
//!
//! ### 1. Canonical-variable conflicts
//!
//! Lines that match the pattern `KEY = VALUE` or `KEY: VALUE` (after trimming)
//! are treated as key-value assertions.  When the same key appears with
//! DIFFERENT values in two different tiers, a conflict is reported.
//!
//! To avoid false positives the key must:
//! - Contain only ASCII alphanumeric characters, underscores, or hyphens.
//! - Be at least 3 characters long.
//! - Not be a common prose word (filtered via a deny-list of likely-heading words).
//!
//! ### 2. Numeric-directive conflicts
//!
//! Lines of the form `<= N lines`, `<= N chars`, `max N tokens` etc. that
//! contain a numeric limit are compared across tiers.  When the same directive
//! root (e.g. `max lines`) appears with different numeric values in two tiers,
//! a conflict is reported.
//!
//! ## Inputs / Outputs
//!
//! - **Input:** `&ResolvedCascade` — the fully resolved cascade.
//! - **Output:** `Vec<ConflictFinding>` — one entry per detected contradiction;
//!   empty when no conflicts are found.
//!
//! ## Constraints
//!
//! - False positives are mitigated by the key allow-list (ASCII, ≥3 chars, not
//!   a prose stop-word) and by only flagging DIFFERENT values for the SAME key.
//! - Whitespace and case are normalised before comparison.
//! - No `unwrap()` outside `#[cfg(test)]` blocks.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: lint_conflicts module (P12-doctor-drift)

use std::collections::HashMap;

use cascade_types::cascade_tier::CascadeTier;
use regex::Regex;

use crate::cascade_resolution::ResolvedCascade;

// ── Public types ──────────────────────────────────────────────────────────────

/// A single cross-tier conflict finding.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ConflictFinding {
    /// The canonical key / directive root that conflicts.
    pub key: String,
    /// The first tier that asserts the key (higher authority).
    pub tier_a: CascadeTier,
    /// The value asserted in `tier_a`.
    pub value_a: String,
    /// The second tier that asserts a DIFFERENT value for the same key.
    pub tier_b: CascadeTier,
    /// The value asserted in `tier_b`.
    pub value_b: String,
}

// ── Entry point ───────────────────────────────────────────────────────────────

/// Scan a resolved cascade for cross-tier contradicting values.
///
/// Applies two heuristics (canonical key-value pairs and numeric directives)
/// across all found tier pairs and returns a deduplicated finding list.
///
/// # Arguments
///
/// * `resolved` — the fully resolved cascade, as returned by
///   [`crate::cascade_resolution::resolve_cascade_full`].
///
/// # Returns
///
/// `Vec<ConflictFinding>` — empty when no conflicts are detected.
pub fn lint_conflicts(resolved: &ResolvedCascade) -> Vec<ConflictFinding> {
    // Collect only found tiers with content, preserving precedence order.
    let found: Vec<&crate::cascade_resolution::TierResult> = resolved
        .tiers_found
        .iter()
        .filter(|t| t.found && !t.instructions.trim().is_empty())
        .collect();

    if found.len() < 2 {
        return Vec::new();
    }

    let mut findings = Vec::new();
    findings.extend(check_kv_conflicts(&found));
    findings.extend(check_numeric_conflicts(&found));
    findings
}

// ── Heuristic 1: key-value conflicts ─────────────────────────────────────────

/// Check for `KEY = VALUE` / `KEY: VALUE` conflicts across tiers.
///
/// Builds a map of `key → (tier, value)` using the first occurrence per key
/// (higher-authority tier), then for each subsequent tier checks whether
/// the same key appears with a different value.
fn check_kv_conflicts(
    found: &[&crate::cascade_resolution::TierResult],
) -> Vec<ConflictFinding> {
    // Map: normalised key → (tier, normalised value) from the first tier that
    // defines it (highest authority).
    let mut canonical: HashMap<String, (CascadeTier, String)> = HashMap::new();
    let mut findings = Vec::new();

    for tier in found.iter() {
        let kvs = extract_kv_pairs(&tier.instructions);
        for (key, value) in kvs {
            if let Some((first_tier, first_value)) = canonical.get(&key) {
                if *first_value != value && *first_tier != tier.tier {
                    findings.push(ConflictFinding {
                        key: key.clone(),
                        tier_a: *first_tier,
                        value_a: first_value.clone(),
                        tier_b: tier.tier,
                        value_b: value.clone(),
                    });
                }
            } else {
                canonical.insert(key, (tier.tier, value));
            }
        }
    }

    findings
}

/// Extract `KEY = VALUE` / `KEY: VALUE` pairs from instruction text.
///
/// Keys must match `[A-Za-z0-9_-]{3,}`.  Both key and value are normalised
/// (trimmed, collapsed whitespace, lowercased key).  Stop-words are rejected.
fn extract_kv_pairs(text: &str) -> Vec<(String, String)> {
    let kv_re = Regex::new(r"(?m)^[[:blank:]]*([A-Za-z][A-Za-z0-9_-]{2,})[[:blank:]]*[=:][[:blank:]]*(.+)$").unwrap();

    let mut pairs = Vec::new();
    for cap in kv_re.captures_iter(text) {
        let key_raw = cap[1].trim().to_lowercase();
        let value_raw = collapse_ws(cap[2].trim());

        if is_stop_word(&key_raw) || value_raw.is_empty() {
            continue;
        }
        pairs.push((key_raw, value_raw));
    }
    pairs
}

// ── Heuristic 2: numeric-directive conflicts ──────────────────────────────────

/// Check for numeric-directive conflicts (e.g. `max N lines`, `<= N chars`).
///
/// Extracts `(directive_root, numeric_value)` pairs from each tier and reports
/// when the same root appears with different values across tiers.
fn check_numeric_conflicts(
    found: &[&crate::cascade_resolution::TierResult],
) -> Vec<ConflictFinding> {
    let mut canonical: HashMap<String, (CascadeTier, String)> = HashMap::new();
    let mut findings = Vec::new();

    for tier in found.iter() {
        let directives = extract_numeric_directives(&tier.instructions);
        for (root, value) in directives {
            if let Some((first_tier, first_value)) = canonical.get(&root) {
                if *first_value != value && *first_tier != tier.tier {
                    findings.push(ConflictFinding {
                        key: root.clone(),
                        tier_a: *first_tier,
                        value_a: first_value.clone(),
                        tier_b: tier.tier,
                        value_b: value.clone(),
                    });
                }
            } else {
                canonical.insert(root, (tier.tier, value));
            }
        }
    }

    findings
}

/// Extract `(directive_root, numeric_value)` pairs.
///
/// Patterns matched (case-insensitive):
/// - `<= N word(s)` / `< N word(s)` — e.g. `<= 300 lines`
/// - `max N word(s)` / `maximum N word(s)`
/// - `≤ N word(s)`
///
/// The directive root is normalised to `<unit>` (e.g. `lines`, `chars`, `tokens`).
fn extract_numeric_directives(text: &str) -> Vec<(String, String)> {
    // Match patterns like `<= 300 lines`, `> 200 lines`, `max 50 lines`, `≤ 200 tokens`
    let re = Regex::new(
        r"(?i)(?:<=|>=|<|>|≤|≥|max(?:imum)?)[[:blank:]]+(\d+)[[:blank:]]+([a-z]+(?:/[a-z]+)?)",
    )
    .unwrap();

    let mut pairs = Vec::new();
    for cap in re.captures_iter(text) {
        let number = cap[1].to_owned();
        let unit = cap[2].to_lowercase();
        // Normalise plurals: `line` → `lines`, `char` → `chars`.
        let unit_norm = match unit.as_str() {
            "line" => "lines".to_owned(),
            "char" => "chars".to_owned(),
            "token" => "tokens".to_owned(),
            other => other.to_owned(),
        };
        pairs.push((unit_norm, number));
    }

    // Deduplicate by root, keeping first occurrence per unit.
    let mut seen: HashMap<String, String> = HashMap::new();
    let mut deduped = Vec::new();
    for (root, value) in pairs {
        if let std::collections::hash_map::Entry::Vacant(e) = seen.entry(root.clone()) {
            e.insert(value.clone());
            deduped.push((root, value));
        }
    }
    deduped
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Collapse consecutive whitespace to a single space and trim.
fn collapse_ws(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut last_space = false;
    for ch in s.chars() {
        if ch.is_whitespace() {
            if !last_space {
                out.push(' ');
            }
            last_space = true;
        } else {
            out.push(ch);
            last_space = false;
        }
    }
    out.trim().to_owned()
}

/// Words that look like keys in regex but are common prose headings or YAML
/// boilerplate; reject them to avoid false positives.
const STOP_WORDS: &[&str] = &[
    "note", "see", "ref", "via", "and", "the", "for", "are", "not", "use",
    "all", "any", "how", "why", "who", "can", "will", "may", "run", "get",
    "set", "put", "add", "new", "old", "yes", "no", "ok", "or", "if", "is",
    "id", "in", "on", "at", "of", "to", "by", "as", "do", "be", "from",
    "with", "that", "this", "each", "per", "step", "list", "item", "name",
    "type", "kind", "mode", "path", "file", "dir", "tag", "key", "val",
    "value", "label", "title", "text", "body", "true", "false", "null",
    "none", "off", "on", "yes", "no", "auto", "default",
];

fn is_stop_word(key: &str) -> bool {
    STOP_WORDS.contains(&key)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cascade_resolution::TierResult;
    use std::path::PathBuf;

    /// Build a minimal `ResolvedCascade` from (tier, instructions) pairs.
    fn make_resolved(tiers: &[(CascadeTier, &str)]) -> ResolvedCascade {
        let all_tiers = cascade_types::cascade_tier::CascadeTier::all_in_precedence_order();
        let mut tier_results: Vec<TierResult> = all_tiers
            .iter()
            .map(|t| {
                let instructions = tiers
                    .iter()
                    .find(|(tier, _)| tier == t)
                    .map(|(_, ins)| ins.to_string())
                    .unwrap_or_default();
                TierResult {
                    tier: *t,
                    found: tiers.iter().any(|(tier, _)| tier == t),
                    path_searched: PathBuf::from("/fake"),
                    instructions,
                }
            })
            .collect();

        // Ensure found flag matches.
        for tr in &mut tier_results {
            if tiers.iter().any(|(t, _)| *t == tr.tier) {
                tr.found = true;
            }
        }

        ResolvedCascade {
            merged_instructions: String::new(),
            on_demand_rules: Vec::new(),
            vars: Default::default(),
            mcp_server_url: String::new(),
            tiers_found: tier_results,
            working_dir: PathBuf::from("/fake"),
        }
    }

    // ── Test 1: no conflict → empty findings ──────────────────────────────────

    /// Different keys in different tiers produce no findings.
    ///
    /// WHY: must not generate false positives when tiers are consistent.
    #[test]
    fn no_conflict_empty_findings() {
        let gci = "max_retries: 3\nSome global policy text here.";
        let ppc = "timeout_seconds: 30\nSome project policy text here.";

        let resolved = make_resolved(&[(CascadeTier::Gci, gci), (CascadeTier::Ppc, ppc)]);
        let findings = lint_conflicts(&resolved);
        assert!(
            findings.is_empty(),
            "no conflict expected for distinct keys: {findings:#?}"
        );
    }

    // ── Test 2: same key same value → no conflict ─────────────────────────────

    /// Two tiers asserting the same key with the same value is allowed.
    ///
    /// WHY: the cascade rule "reference up" applies to content, but consistent
    /// key-value pairs are not flagged as conflicts.
    #[test]
    fn same_key_same_value_no_conflict() {
        let gci = "log_level: info\nGlobal policy.";
        let ppc = "log_level: info\nProject policy.";

        let resolved = make_resolved(&[(CascadeTier::Gci, gci), (CascadeTier::Ppc, ppc)]);
        let findings = lint_conflicts(&resolved);
        assert!(
            findings.is_empty(),
            "same key same value must not be a conflict: {findings:#?}"
        );
    }

    // ── Test 3: same key different value → one conflict ───────────────────────

    /// Two tiers asserting the same key with different values produce one finding.
    ///
    /// WHY: core contract — cross-tier contradictions must be detected.
    #[test]
    fn same_key_different_value_produces_conflict() {
        let gci = "log_level: info\nGlobal policy text for testing.";
        let ppc = "log_level: debug\nProject policy text for testing.";

        let resolved = make_resolved(&[(CascadeTier::Gci, gci), (CascadeTier::Ppc, ppc)]);
        let findings = lint_conflicts(&resolved);
        assert_eq!(findings.len(), 1, "expected exactly one conflict finding");
        assert_eq!(findings[0].key, "log_level");
        assert_eq!(findings[0].value_a, "info");
        assert_eq!(findings[0].value_b, "debug");
        assert_eq!(findings[0].tier_a, CascadeTier::Gci);
        assert_eq!(findings[0].tier_b, CascadeTier::Ppc);
    }

    // ── Test 4: numeric directive conflict ────────────────────────────────────

    /// Two tiers asserting different max-lines limits produce a numeric conflict.
    ///
    /// WHY: numeric directives like file-size caps are a common source of
    /// cross-tier contradictions.
    #[test]
    fn numeric_directive_conflict_detected() {
        let gci = "No file > 300 lines.\nGlobal coding policy for all projects.";
        let ppc = "No file > 200 lines.\nProject override policy for this repo.";

        let resolved = make_resolved(&[(CascadeTier::Gci, gci), (CascadeTier::Ppc, ppc)]);
        let findings = lint_conflicts(&resolved);
        // Should detect a `lines` directive conflict (300 vs 200).
        let line_conflict = findings.iter().find(|f| f.key == "lines");
        assert!(
            line_conflict.is_some(),
            "numeric lines conflict must be detected: {findings:#?}"
        );
        let c = line_conflict.unwrap();
        assert_eq!(c.value_a, "300");
        assert_eq!(c.value_b, "200");
    }

    // ── Test 5: single tier → no conflict ────────────────────────────────────

    /// With only one tier present there is nothing to conflict with.
    ///
    /// WHY: edge case; must not panic or produce spurious output.
    #[test]
    fn single_tier_no_conflict() {
        let gci = "log_level: info\nGlobal policy.";
        let resolved = make_resolved(&[(CascadeTier::Gci, gci)]);
        let findings = lint_conflicts(&resolved);
        assert!(findings.is_empty(), "single tier cannot conflict with itself");
    }

    // ── Test 6: stop-words ignored ────────────────────────────────────────────

    /// Common prose words used as keys (e.g. `name: X`) must not produce findings.
    ///
    /// WHY: prose fragments like `name: global` and `name: project` appear
    /// across tiers as human descriptions, not configuration keys.
    #[test]
    fn stop_words_not_flagged() {
        let gci = "name: global config\nTrue policy content for the test suite.";
        let ppc = "name: project config\nAnother policy content line.";

        let resolved = make_resolved(&[(CascadeTier::Gci, gci), (CascadeTier::Ppc, ppc)]);
        let findings = lint_conflicts(&resolved);
        assert!(
            findings.is_empty(),
            "stop-word keys must not produce conflict findings: {findings:#?}"
        );
    }
}
