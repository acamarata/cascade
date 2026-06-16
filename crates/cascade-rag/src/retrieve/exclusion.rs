//! Scope exclusion for the retrieval pipeline.
//!
//! Implements defense-in-depth privacy isolation: locked zones (e.g. medical,
//! legal, financial threads) must never appear in any retrieval result regardless
//! of which channel surfaces them.
//!
//! ## 2-layer architecture
//!
//! **Layer 1 (pre-filter):** the exclusion set is threaded into every retrieval
//! channel before queries are run.  Low-level helpers (`is_excluded`,
//! `filter_pairs`) are called on the raw `(chunk_id, source_path)` rows returned
//! by FTS, vector, curated, and recency channels — excluded docs never enter the
//! RRF candidate lists.
//!
//! **Layer 2 (post-filter):** after RRF fusion [`ExclusionSet::filter_hits`] is
//! applied to the final `Vec<RetrievalHit>` list.  This is the belt-and-suspenders
//! pass: even if a channel leaks a hit past layer 1 (e.g. due to an ID translation
//! edge case), the post-filter removes it before results are returned to the caller.
//!
//! ## Matching rules
//!
//! - Patterns are **glob strings** over canonical source paths/IDs
//!   (e.g. `"/home/user/medical/**"`, `"legal/*"`, `"private/"`).
//! - A **prefix pattern** (ending in `/` or `/**`) excludes every path that starts
//!   with the prefix after stripping the trailing `/**` or `/`.
//! - A **literal pattern** (no wildcards) matches if the source path *starts with*
//!   the pattern string — this preserves the intuition that a locked directory
//!   excludes all children.
//! - Matching is **case-sensitive** (paths are OS paths; case folding could
//!   silently miss a variant).
//! - An empty `ExclusionConfig` (no patterns) is a no-op: zero filtering applied.
//!
//! ## Caller contract
//!
//! The resolver / daemon constructs an `ExclusionConfig` from the user's
//! `cascade.toml` `[privacy]` section (or an equivalent runtime config source)
//! and passes it to [`RrfRetriever::new_with_exclusion`].  The retriever holds an
//! [`ExclusionSet`] compiled from that config for the lifetime of the retriever.
//! Callers supply `source_path` alongside `chunk_id` at the point of
//! post-filtering; for pre-filtering the path is available from the DB row.
//!
//! ## Example
//!
//! ```rust
//! use cascade_rag::retrieve::exclusion::{ExclusionConfig, ExclusionSet};
//!
//! let cfg = ExclusionConfig {
//!     patterns: vec![
//!         "/home/user/medical/".to_string(),
//!         "/home/user/legal/**".to_string(),
//!     ],
//! };
//! let ex = ExclusionSet::compile(&cfg);
//! assert!(ex.is_excluded("/home/user/medical/appointments.md"));
//! assert!(ex.is_excluded("/home/user/legal/contract.pdf"));
//! assert!(!ex.is_excluded("/home/user/notes/shopping.md"));
//! ```
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve::ExclusionSet

use cascade_types::retriever::RetrievalHit;
use serde::{Deserialize, Serialize};

// ── Config ────────────────────────────────────────────────────────────────────

/// Caller-supplied exclusion configuration.
///
/// Typically loaded from `cascade.toml` `[privacy]` and passed to
/// [`RrfRetriever::new_with_exclusion`].  An empty `patterns` list means no
/// filtering — the retriever runs without restriction.
///
/// # Glob semantics
///
/// Each pattern is matched against the canonical source path or chunk ID using
/// [`ExclusionSet::is_excluded`].  See module-level docs for matching rules.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct ExclusionConfig {
    /// Glob / prefix patterns over source paths.  Each matching path is locked.
    pub patterns: Vec<String>,
}

impl ExclusionConfig {
    /// Convenience constructor: create a config from a slice of pattern strings.
    pub fn from_patterns(patterns: impl IntoIterator<Item = impl Into<String>>) -> Self {
        Self {
            patterns: patterns.into_iter().map(Into::into).collect(),
        }
    }
}

// ── Compiled exclusion set ────────────────────────────────────────────────────

/// Compiled, query-ready form of [`ExclusionConfig`].
///
/// `compile` pre-processes each pattern into one of two internal forms so that
/// [`is_excluded`] is a simple string-prefix check — no glob engine needed.
///
/// # Design note
///
/// Full glob matching (e.g. `fnmatch`) would allow patterns like `*.key` to
/// match anywhere in a tree.  We intentionally limit the pattern language to
/// **prefix/subtree patterns** because:
///
/// - Privacy zones are always subtrees (a directory and all its descendants).
/// - Wildcard-suffix patterns like `secret_*.md` are error-prone: a rename or
///   new file escapes the lock silently.
/// - Prefix matching is O(len) and branchless — no heap allocation per check.
///
/// Callers that need suffix/regex exclusion should pre-compute the locked set
/// and pass it as explicit path prefixes.
#[derive(Debug, Clone, Default)]
pub struct ExclusionSet {
    /// Normalised prefix strings.  A path is excluded if it starts with any of
    /// these.  Case-sensitive.  Empty = no exclusion.
    prefixes: Vec<String>,
}

impl ExclusionSet {
    /// Compile an [`ExclusionConfig`] into a query-ready `ExclusionSet`.
    ///
    /// # Normalisation
    ///
    /// | Input pattern | Normalised prefix |
    /// |---|---|
    /// | `/home/user/medical/**` | `/home/user/medical/` |
    /// | `/home/user/medical/` | `/home/user/medical/` |
    /// | `/home/user/medical` | `/home/user/medical` |
    ///
    /// Trailing `/**` is stripped; the remaining string is used verbatim as a
    /// prefix.  This means `/home/user/medical` excludes both the exact path
    /// `/home/user/medical` and any path starting with `/home/user/medical`
    /// (e.g. `/home/user/medical/appointments.md`).
    pub fn compile(config: &ExclusionConfig) -> Self {
        let prefixes = config
            .patterns
            .iter()
            .filter(|p| !p.is_empty())
            .map(|p| {
                // Strip trailing `/**` glob suffix — keep the directory part.
                if p.ends_with("/**") {
                    p[..p.len() - 3].to_string()
                } else {
                    p.clone()
                }
            })
            .collect();
        Self { prefixes }
    }

    /// Return `true` if `source_path` is covered by any exclusion prefix.
    ///
    /// # Semantics
    ///
    /// `source_path` matches when it:
    ///
    /// 1. **Equals** a prefix exactly, OR
    /// 2. **Starts with** a prefix followed by `/` (subtree match).
    ///
    /// The subtree check (`starts_with(prefix + "/")`) prevents the pattern
    /// `/home/user/med` from accidentally matching `/home/user/medical/x.md`.
    ///
    /// # Example
    ///
    /// ```
    /// use cascade_rag::retrieve::exclusion::{ExclusionConfig, ExclusionSet};
    ///
    /// let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked/"]));
    /// assert!(ex.is_excluded("/locked/file.md"));
    /// assert!(ex.is_excluded("/locked/"));
    /// assert!(!ex.is_excluded("/locked-other/file.md"));
    /// ```
    pub fn is_excluded(&self, source_path: &str) -> bool {
        for prefix in &self.prefixes {
            // Exact match (the path IS the locked root).
            if source_path == prefix.as_str() {
                return true;
            }
            // Subtree match: path starts with `<prefix>/`  OR  `<prefix>` when
            // the prefix itself already ends with `/`.
            let sep = if prefix.ends_with('/') { "" } else { "/" };
            let qualified = format!("{prefix}{sep}");
            if source_path.starts_with(qualified.as_str()) {
                return true;
            }
        }
        false
    }

    /// Return `true` when no exclusion patterns are configured.
    ///
    /// Used as a fast-path: callers skip filtering overhead when the set is empty.
    pub fn is_empty(&self) -> bool {
        self.prefixes.is_empty()
    }

    /// Filter a `(chunk_id, score)` list, removing entries whose `source_path`
    /// is excluded.
    ///
    /// # Inputs
    ///
    /// - `pairs`        — raw `(chunk_id, score)` output from a retrieval channel.
    /// - `path_for_id`  — closure mapping `chunk_id → Option<&str>` (the source
    ///   path for that chunk).  Return `None` when the mapping is unavailable;
    ///   chunks with `None` paths are **kept** (fail-open for non-path IDs).
    ///
    /// # Outputs
    ///
    /// Filtered `Vec<(i64, f64)>` with all excluded chunks removed, order preserved.
    pub fn filter_pairs<F>(&self, pairs: &[(i64, f64)], path_for_id: F) -> Vec<(i64, f64)>
    where
        F: Fn(i64) -> Option<String>,
    {
        if self.is_empty() {
            return pairs.to_vec();
        }
        pairs
            .iter()
            .filter(|(id, _)| match path_for_id(*id) {
                Some(path) => !self.is_excluded(&path),
                None => true, // unknown path → keep (fail-open)
            })
            .copied()
            .collect()
    }

    /// Layer-2 post-filter: remove any [`RetrievalHit`] whose `file_path` is
    /// in an excluded zone.
    ///
    /// This is the belt-and-suspenders pass applied to the fully fused result
    /// list before returning to the caller.  Hits with no `file_path` are kept
    /// (fail-open: they cannot be matched against a path pattern).
    ///
    /// # Inputs
    ///
    /// - `hits` — fully fused and ranked `Vec<RetrievalHit>`.
    ///
    /// # Outputs
    ///
    /// Filtered hit list.  Ranks are NOT re-numbered — callers should re-index
    /// `rank` after filtering if sequential ranks are required.
    pub fn filter_hits(&self, hits: Vec<RetrievalHit>) -> Vec<RetrievalHit> {
        if self.is_empty() {
            return hits;
        }
        hits.into_iter()
            .filter(|h| match &h.file_path {
                Some(path) => match path.to_str() {
                    Some(s) => !self.is_excluded(s),
                    None => true, // non-UTF-8 path → keep (cannot match)
                },
                None => true, // no path metadata → keep (fail-open)
            })
            .collect()
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    // ── exclusion::prefix_matching ────────────────────────────────────────────

    /// Exact match on the prefix itself is excluded.
    #[test]
    fn exact_prefix_excluded() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
        assert!(ex.is_excluded("/locked"), "exact prefix must be excluded");
    }

    /// A child path under the prefix is excluded.
    #[test]
    fn subtree_excluded() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
        assert!(
            ex.is_excluded("/locked/secret.md"),
            "child of prefix must be excluded"
        );
        assert!(
            ex.is_excluded("/locked/sub/dir/file.txt"),
            "deep child of prefix must be excluded"
        );
    }

    /// A path that only shares a string prefix but NOT a directory boundary is
    /// NOT excluded — prevents `/med` from matching `/medical/`.
    #[test]
    fn no_partial_name_match() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/med"]));
        assert!(
            !ex.is_excluded("/medical/file.md"),
            "/medical/ must NOT match prefix /med (no dir boundary)"
        );
    }

    /// Trailing `/**` is normalised to the directory prefix.
    #[test]
    fn glob_suffix_normalised() {
        let ex =
            ExclusionSet::compile(&ExclusionConfig::from_patterns(["/home/user/medical/**"]));
        assert!(
            ex.is_excluded("/home/user/medical/appointment.md"),
            "child must be excluded after glob normalisation"
        );
        assert!(
            !ex.is_excluded("/home/user/notes/shopping.md"),
            "sibling dir must not be excluded"
        );
    }

    /// Trailing `/` in pattern works the same as no trailing slash.
    #[test]
    fn trailing_slash_pattern() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked/"]));
        assert!(ex.is_excluded("/locked/file.md"));
        assert!(ex.is_excluded("/locked/"));
        assert!(!ex.is_excluded("/locked-other/file.md"));
    }

    /// Empty config = no filtering (fast path).
    #[test]
    fn empty_config_no_filtering() {
        let ex = ExclusionSet::compile(&ExclusionConfig::default());
        assert!(ex.is_empty(), "empty config must produce empty set");
        assert!(
            !ex.is_excluded("/any/path.md"),
            "empty set must exclude nothing"
        );
    }

    /// Multiple patterns: a path matching ANY pattern is excluded.
    #[test]
    fn multiple_patterns_any_match_excluded() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns([
            "/medical",
            "/legal",
            "/financial",
        ]));
        assert!(ex.is_excluded("/medical/records.pdf"));
        assert!(ex.is_excluded("/legal/contracts/2026.pdf"));
        assert!(ex.is_excluded("/financial/tax.xlsx"));
        assert!(!ex.is_excluded("/notes/shopping.md"));
    }

    // ── exclusion::filter_pairs ───────────────────────────────────────────────

    /// filter_pairs removes excluded IDs and keeps non-excluded ones.
    #[test]
    fn filter_pairs_removes_excluded() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));

        // chunk 1 → /locked/secret.md (excluded)
        // chunk 2 → /public/safe.md   (kept)
        // chunk 3 → no path known     (kept, fail-open)
        let pairs = vec![(1i64, 0.9), (2, 0.8), (3, 0.7)];
        let filtered = ex.filter_pairs(&pairs, |id| match id {
            1 => Some("/locked/secret.md".to_string()),
            2 => Some("/public/safe.md".to_string()),
            _ => None,
        });

        assert_eq!(filtered.len(), 2, "chunk 1 must be removed");
        assert!(filtered.iter().any(|(id, _)| *id == 2));
        assert!(filtered.iter().any(|(id, _)| *id == 3));
        assert!(!filtered.iter().any(|(id, _)| *id == 1));
    }

    /// filter_pairs with empty exclusion set returns input unchanged.
    #[test]
    fn filter_pairs_empty_set_passthrough() {
        let ex = ExclusionSet::compile(&ExclusionConfig::default());
        let pairs = vec![(1i64, 0.9), (2, 0.8)];
        let filtered = ex.filter_pairs(&pairs, |_| Some("/locked/file.md".to_string()));
        assert_eq!(filtered, pairs, "empty set must not filter anything");
    }

    // ── exclusion::filter_hits ────────────────────────────────────────────────

    fn make_hit(chunk_id: &str, file_path: Option<&str>, score: f32) -> RetrievalHit {
        RetrievalHit {
            chunk_id: chunk_id.to_string(),
            text: "body text".to_string(),
            file_path: file_path.map(PathBuf::from),
            start_line: None,
            end_line: None,
            score,
            rank: 0,
            tier: None,
        }
    }

    /// filter_hits removes hits whose file_path is in an excluded zone.
    #[test]
    fn filter_hits_removes_excluded() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/private"]));

        let hits = vec![
            make_hit("1", Some("/private/diary.md"), 0.95),
            make_hit("2", Some("/public/notes.md"), 0.85),
            make_hit("3", None, 0.75), // no path → kept (fail-open)
        ];

        let filtered = ex.filter_hits(hits);
        assert_eq!(filtered.len(), 2, "locked hit must be removed");
        assert!(filtered.iter().any(|h| h.chunk_id == "2"));
        assert!(filtered.iter().any(|h| h.chunk_id == "3"));
        assert!(!filtered.iter().any(|h| h.chunk_id == "1"));
    }

    /// filter_hits with empty exclusion set returns input unchanged.
    #[test]
    fn filter_hits_empty_set_passthrough() {
        let ex = ExclusionSet::compile(&ExclusionConfig::default());
        let hits = vec![
            make_hit("1", Some("/locked/secret.md"), 0.9),
            make_hit("2", Some("/public/notes.md"), 0.8),
        ];
        let count_before = hits.len();
        let filtered = ex.filter_hits(hits);
        assert_eq!(
            filtered.len(),
            count_before,
            "empty set must not filter anything"
        );
    }

    // ── exclusion::case_sensitivity ───────────────────────────────────────────

    /// Matching is case-sensitive: a different case variant is NOT excluded.
    #[test]
    fn case_sensitive_no_false_positive() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/Locked"]));
        assert!(ex.is_excluded("/Locked/file.md"), "/Locked/ must be excluded");
        assert!(
            !ex.is_excluded("/locked/file.md"),
            "/locked/ (different case) must NOT be excluded"
        );
    }
}
