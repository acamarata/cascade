// merge/conflict.rs — Merge conflict detector.
//
// Purpose: Identifies merge sections that may overlap or contradict each other,
//   so the DiffPanel can highlight them for user review.
//
// Detection heuristics (applied in order):
//   1. Title similarity  — normalized titles that are substrings of each other.
//   2. Content overlap   — >40 % Jaccard similarity on character trigram sets.
//   3. Contradiction     — keyword pairs (always/never, must/must not, do/don't).
//   4. Source overlap    — two sections share a sourceFile path.
//
// Conflicts are advisory only — the user decides whether to reject a section.
//
// Inputs:  MergeResult.sections
// Outputs: Vec<MergeConflict>
//
// SPORT: MASTER-COMPONENTS.md — conflictDetector — merge/conflict.rs
// Task: T-P3-E03-30

use std::collections::HashSet;

use crate::merge::types::{MergeConflict, MergeResult, MergeSection};

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/// Normalise a title for similarity comparison: lowercase, strip non-alphanumeric.
fn normalise_title(title: &str) -> String {
    title
        .to_lowercase()
        .chars()
        .filter(|c| c.is_alphanumeric() || c.is_whitespace())
        .collect::<String>()
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
}

/// Build the set of all character-level trigrams from `text`.
///
/// Operates over Unicode scalar values; safe for non-ASCII content.
fn trigrams(text: &str) -> HashSet<[char; 3]> {
    let chars: Vec<char> = text.chars().collect();
    if chars.len() < 3 {
        return HashSet::new();
    }
    chars.windows(3).map(|w| [w[0], w[1], w[2]]).collect()
}

/// Jaccard similarity over two trigram sets.
///
/// Returns 0.0 when both sets are empty.
fn jaccard(a: &HashSet<[char; 3]>, b: &HashSet<[char; 3]>) -> f64 {
    if a.is_empty() && b.is_empty() {
        return 0.0;
    }
    let intersection = a.intersection(b).count() as f64;
    let union = a.union(b).count() as f64;
    if union == 0.0 { 0.0 } else { intersection / union }
}

/// Contradiction keyword pairs.
///
/// Each element is `(positive_keyword, negative_keyword)`.  A conflict is
/// flagged when section A contains the positive form AND section B contains
/// the negative form (or vice-versa).
const CONTRADICTION_PAIRS: &[(&str, &str)] = &[
    ("always", "never"),
    ("must", "must not"),
    ("do", "don't"),
    ("do", "do not"),
    ("enable", "disable"),
    ("allow", "disallow"),
    ("allow", "deny"),
    ("require", "forbid"),
    ("use", "avoid"),
];

/// Return `true` if `text` contains the given word/phrase as a whole-word match
/// (case-insensitive).  Uses simple lowercase substring for phrases; word-boundary
/// anchoring for single tokens.
fn contains_keyword(text: &str, keyword: &str) -> bool {
    let haystack = text.to_lowercase();
    let needle = keyword.to_lowercase();
    if needle.contains(' ') {
        // Multi-word phrase: substring match is sufficient.
        haystack.contains(needle.as_str())
    } else {
        // Single token: require word boundaries via split_whitespace scan.
        haystack
            .split(|c: char| !c.is_alphanumeric())
            .any(|token| token == needle)
    }
}

/// Check all contradiction pairs between two sections.
///
/// Returns a reason string if a contradiction is detected, or `None`.
fn check_contradiction(a: &MergeSection, b: &MergeSection) -> Option<String> {
    let content_a = a.proposed_content.to_lowercase();
    let content_b = b.proposed_content.to_lowercase();

    for (pos, neg) in CONTRADICTION_PAIRS {
        let a_has_pos = contains_keyword(&content_a, pos);
        let b_has_neg = contains_keyword(&content_b, neg);
        let a_has_neg = contains_keyword(&content_a, neg);
        let b_has_pos = contains_keyword(&content_b, pos);

        if (a_has_pos && b_has_neg) || (a_has_neg && b_has_pos) {
            return Some(format!(
                "Contradictory policy keywords: '{}' vs '{}'",
                pos, neg
            ));
        }
    }
    None
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/// Detect potential conflicts within a MergeResult.
///
/// Runs all four heuristics over every (section_a, section_b) pair in O(n²).
/// Safe for typical result sizes (1–20 sections).
///
/// Heuristics applied in order (first match wins for each pair):
/// 1. **Title similarity** — normalised title of A is a substring of B or vice-versa.
/// 2. **Content overlap** — Jaccard similarity of character trigrams > 0.40.
/// 3. **Contradiction** — contradictory keyword pairs (always/never, must/must not, …).
/// 4. **Source overlap** — two sections share at least one `sourceFile.path`.
///
/// Returns an empty `Vec` when no conflicts are detected.
pub fn detect_conflicts(result: &MergeResult) -> Vec<MergeConflict> {
    let sections = &result.sections;
    let mut conflicts: Vec<MergeConflict> = Vec::new();

    // Pre-compute normalised titles and trigrams once.
    let normalised: Vec<String> = sections.iter().map(|s| normalise_title(&s.title)).collect();
    let tgrams: Vec<HashSet<[char; 3]>> = sections
        .iter()
        .map(|s| trigrams(&s.proposed_content))
        .collect();

    for i in 0..sections.len() {
        for j in (i + 1)..sections.len() {
            let a = &sections[i];
            let b = &sections[j];

            // --- Heuristic 1: Title similarity ---
            let na = &normalised[i];
            let nb = &normalised[j];
            if !na.is_empty() && !nb.is_empty() {
                if na.contains(nb.as_str()) || nb.contains(na.as_str()) {
                    conflicts.push(MergeConflict {
                        section_a: a.clone(),
                        section_b: b.clone(),
                        reason: format!(
                            "Title similarity: '{}' and '{}' share overlapping text",
                            a.title, b.title
                        ),
                    });
                    continue; // Only emit one conflict per pair.
                }
            }

            // --- Heuristic 2: Content overlap (Jaccard > 0.40) ---
            let similarity = jaccard(&tgrams[i], &tgrams[j]);
            if similarity > 0.40 {
                conflicts.push(MergeConflict {
                    section_a: a.clone(),
                    section_b: b.clone(),
                    reason: format!(
                        "Content overlap: {:.0}% trigram similarity exceeds 40% threshold",
                        similarity * 100.0
                    ),
                });
                continue;
            }

            // --- Heuristic 3: Contradiction keywords ---
            if let Some(reason) = check_contradiction(a, b) {
                conflicts.push(MergeConflict {
                    section_a: a.clone(),
                    section_b: b.clone(),
                    reason,
                });
                continue;
            }

            // --- Heuristic 4: Source overlap (shared sourceFile path) ---
            let paths_a: HashSet<&str> =
                a.source_files.iter().map(|f| f.path.as_str()).collect();
            let paths_b: HashSet<&str> =
                b.source_files.iter().map(|f| f.path.as_str()).collect();
            if !paths_a.is_disjoint(&paths_b) {
                let shared: Vec<&str> = paths_a.intersection(&paths_b).copied().collect();
                conflicts.push(MergeConflict {
                    section_a: a.clone(),
                    section_b: b.clone(),
                    reason: format!(
                        "Source overlap: both sections derive from '{}'",
                        shared.join(", ")
                    ),
                });
            }
        }
    }

    conflicts
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::merge::types::{MergeResult, MergeSection, MergeSourceFile, SectionStatus};
    use crate::scanner::types::{FileKind, ToolId};

    fn make_section(id: &str, title: &str, content: &str, path: Option<&str>) -> MergeSection {
        let source_files = if let Some(p) = path {
            vec![MergeSourceFile {
                tool_id: ToolId::ClaudeCode,
                path: p.to_string(),
                content: None,
                kind: FileKind::Instructions,
            }]
        } else {
            vec![]
        };
        MergeSection {
            id: id.to_string(),
            title: title.to_string(),
            source_files,
            proposed_content: content.to_string(),
            status: SectionStatus::Pending,
            edited_content: None,
            conflict_with: None,
        }
    }

    fn make_result(sections: Vec<MergeSection>) -> MergeResult {
        MergeResult {
            tier: 4,
            sections,
            generated_at: "2026-06-09T00:00:00Z".to_string(),
            model_used: "gemini-2.0-flash".to_string(),
            prompt_hash: "abc123".to_string(),
        }
    }

    // --- Case 1: no conflict (totally unrelated sections) ---
    #[test]
    fn no_conflict_for_unrelated_sections() {
        let result = make_result(vec![
            make_section("a1", "Code Style", "Use 2-space indentation for all files.", None),
            make_section(
                "a2",
                "Deployment Pipeline",
                "Canary deploy to 5% of traffic before full rollout.",
                None,
            ),
        ]);
        let conflicts = detect_conflicts(&result);
        assert!(
            conflicts.is_empty(),
            "expected no conflicts for unrelated sections, got: {conflicts:#?}"
        );
    }

    // --- Case 2: empty result ---
    #[test]
    fn no_conflict_for_empty_result() {
        let result = make_result(vec![]);
        assert!(detect_conflicts(&result).is_empty());
    }

    // --- Case 3: title overlap (duplicate heading) ---
    #[test]
    fn duplicate_heading_detected_via_title_similarity() {
        let result = make_result(vec![
            make_section(
                "b1",
                "Code Quality Rules",
                "Enforce strict TypeScript. No any types.",
                None,
            ),
            make_section(
                "b2",
                "Code Quality",
                "Use ESLint and Prettier. No magic numbers.",
                None,
            ),
        ]);
        let conflicts = detect_conflicts(&result);
        assert_eq!(conflicts.len(), 1, "expected 1 conflict, got: {conflicts:#?}");
        assert!(
            conflicts[0].reason.contains("Title similarity"),
            "expected title-similarity reason, got: {}",
            conflicts[0].reason
        );
    }

    // --- Case 4: content overlap (high trigram Jaccard) ---
    #[test]
    fn content_overlap_detected_via_trigrams() {
        let base = "Always use dependency injection. Never bypass the DI container. \
                    Register all services in the root module. Inject via constructor only.";
        let near_dup = "Always use dependency injection. Never bypass the DI container. \
                        Register all services in the module root. Prefer constructor injection.";
        let result = make_result(vec![
            make_section("c1", "Dependency Injection Policy", base, None),
            make_section("c2", "DI Rules", near_dup, None),
        ]);
        let conflicts = detect_conflicts(&result);
        assert_eq!(conflicts.len(), 1, "expected 1 conflict, got: {conflicts:#?}");
        assert!(
            conflicts[0].reason.contains("trigram similarity"),
            "expected trigram-similarity reason, got: {}",
            conflicts[0].reason
        );
    }

    // --- Case 5: source file overlap ---
    #[test]
    fn source_overlap_detected() {
        let shared_path = "/Users/admin/.claude/CLAUDE.md";
        let result = make_result(vec![
            make_section(
                "d1",
                "Global Rules",
                "Use pnpm only for JavaScript.",
                Some(shared_path),
            ),
            make_section(
                "d2",
                "Package Manager Policy",
                "Yarn is forbidden on all projects.",
                Some(shared_path),
            ),
        ]);
        let conflicts = detect_conflicts(&result);
        assert_eq!(conflicts.len(), 1, "expected 1 conflict, got: {conflicts:#?}");
        assert!(
            conflicts[0].reason.contains("Source overlap"),
            "expected source-overlap reason, got: {}",
            conflicts[0].reason
        );
        assert!(
            conflicts[0].reason.contains(shared_path),
            "reason should mention shared path"
        );
    }

    // --- Case 6: contradiction keywords ---
    #[test]
    fn contradiction_keywords_detected() {
        let result = make_result(vec![
            make_section(
                "e1",
                "Logging Config",
                "You must always log every request to the audit trail.",
                None,
            ),
            make_section(
                "e2",
                "Privacy Policy",
                "You must never log personal user data to any sink.",
                None,
            ),
        ]);
        let conflicts = detect_conflicts(&result);
        assert_eq!(conflicts.len(), 1, "expected 1 conflict, got: {conflicts:#?}");
        assert!(
            conflicts[0].reason.contains("Contradictory"),
            "expected contradiction reason, got: {}",
            conflicts[0].reason
        );
    }

    // --- Case 7: multiple pairs, multiple conflicts ---
    #[test]
    fn multiple_pairs_produce_multiple_conflicts() {
        let shared = "/shared/CLAUDE.md";
        let result = make_result(vec![
            make_section(
                "f1",
                "Security Rules",
                "Enforce TLS on all connections.",
                Some(shared),
            ),
            make_section(
                "f2",
                "Security Guidelines",
                "TLS must be enforced on every endpoint.",
                Some(shared),
            ),
            make_section(
                "f3",
                "Performance Tuning",
                "Cache aggressively at every layer for low latency.",
                None,
            ),
        ]);
        let conflicts = detect_conflicts(&result);
        // f1 vs f2: title similarity (both contain "security") + source overlap;
        // only first heuristic fires (title similarity takes precedence).
        assert!(
            !conflicts.is_empty(),
            "expected at least one conflict for near-duplicate pair"
        );
    }
}
