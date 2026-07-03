//! # taxonomy — auto-classify memory/instruction snippets into category tags
//!
//! ## Purpose
//!
//! Assigns structured category tags to a text snippet so memory entries can be
//! routed to the correct memory file and indexed with meaningful labels.
//!
//! Two classification paths exist:
//! - **Rule-based** (always available): keyword/pattern matching. Deterministic,
//!   zero external dependencies. Used as the primary path and as the fallback
//!   when GFP is unavailable.
//! - **GFP-assisted** (optional): routes the classification prompt through the
//!   routing matrix's `Cheap` lane (GFP free Flash). Falls back to rule-based
//!   when the lane returns `Unavailable`.
//!
//! ## Outputs
//!
//! A `Vec<Tag>` — one or more tags assigned to the snippet.
//!
//! ## Constraints
//!
//! - No `unwrap()` outside `#[cfg(test)]`.
//! - GFP path is optional; tests MUST pass without any provider configured.
//! - File ≤ 300 lines.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: taxonomy

use std::fmt;

// ── Tag ───────────────────────────────────────────────────────────────────────

/// A structured tag assigned to a memory/instruction snippet.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub enum Tag {
    // ── Primary category (exactly one per entry) ───────────────────────────
    /// A significant technical or architectural choice made during the work.
    Decision,
    /// A gotcha, pitfall, or hard-won learning to avoid repeating.
    Lesson,
    /// A stable convention, idiom, or recurring pattern in the codebase.
    Pattern,

    // ── Domain tags (zero or more per entry) ──────────────────────────────
    /// Relates to security, credentials, or access control.
    Security,
    /// Relates to performance, latency, or throughput optimisation.
    Performance,
    /// Relates to API design, HTTP routes, or data contracts.
    Api,
    /// Relates to testing, CI, or quality gates.
    Testing,
    /// Relates to documentation or specification writing.
    Docs,
    /// Relates to dependency management or version constraints.
    Dependencies,
    /// Relates to configuration, settings, or environment setup.
    Config,
    /// Relates to error handling, recovery, or fault tolerance.
    Errors,
    /// Relates to data modelling, schema design, or migrations.
    DataModel,
    /// A tag not covered by any of the above domain categories.
    Custom(String),
}

impl fmt::Display for Tag {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let s = match self {
            Tag::Decision => "decision",
            Tag::Lesson => "lesson",
            Tag::Pattern => "pattern",
            Tag::Security => "security",
            Tag::Performance => "performance",
            Tag::Api => "api",
            Tag::Testing => "testing",
            Tag::Docs => "docs",
            Tag::Dependencies => "dependencies",
            Tag::Config => "config",
            Tag::Errors => "errors",
            Tag::DataModel => "data-model",
            Tag::Custom(s) => s.as_str(),
        };
        write!(f, "{}", s)
    }
}

impl Tag {
    /// Returns `true` if this tag is a primary category (Decision/Lesson/Pattern).
    pub fn is_primary(&self) -> bool {
        matches!(self, Tag::Decision | Tag::Lesson | Tag::Pattern)
    }

    /// Returns the memory file name this primary tag maps to (without extension).
    ///
    /// Returns `None` for domain tags.
    pub fn memory_file(&self) -> Option<&'static str> {
        match self {
            Tag::Decision => Some("decisions"),
            Tag::Lesson => Some("lessons"),
            Tag::Pattern => Some("patterns"),
            _ => None,
        }
    }
}

// ── Rule-based classification ─────────────────────────────────────────────────

/// Classify `text` using deterministic keyword/pattern rules.
///
/// Always succeeds regardless of provider availability. Returns at least one
/// primary category tag and zero or more domain tags.
///
/// # Examples
/// ```
/// use cascade_core::taxonomy::{classify_topic_rule_based, Tag};
/// let tags = classify_topic_rule_based("chose to use sqlite over postgres for simplicity");
/// assert!(tags.contains(&Tag::Decision));
/// ```
pub fn classify_topic_rule_based(text: &str) -> Vec<Tag> {
    let lower = text.to_lowercase();

    let primary = detect_primary(&lower);
    let mut tags = vec![primary];
    tags.extend(detect_domain(&lower));
    tags
}

fn detect_primary(lower: &str) -> Tag {
    // Decision signals
    let decision_signals = [
        "decided", "chose", "decision", "picked", "selected", "opted",
        "rationale", "trade-off", "tradeoff", "we will", "we won't",
        "going with", "replaced", "switched",
    ];
    // Lesson signals
    let lesson_signals = [
        "learned", "lesson", "gotcha", "pitfall", "mistake", "bug",
        "never", "avoid", "don't", "watch out", "warning", "issue",
        "discovered", "found that", "turns out", "broke", "failed",
    ];
    // Pattern signals
    let pattern_signals = [
        "pattern", "convention", "always", "standard", "idiom",
        "approach", "best practice", "prefer", "use the", "whenever",
        "every time", "template", "boilerplate",
    ];

    let decision_score = decision_signals.iter().filter(|&&kw| lower.contains(kw)).count();
    let lesson_score = lesson_signals.iter().filter(|&&kw| lower.contains(kw)).count();
    let pattern_score = pattern_signals.iter().filter(|&&kw| lower.contains(kw)).count();

    if decision_score >= lesson_score && decision_score >= pattern_score {
        Tag::Decision
    } else if lesson_score >= pattern_score {
        Tag::Lesson
    } else {
        Tag::Pattern
    }
}

fn detect_domain(lower: &str) -> Vec<Tag> {
    let mut tags = Vec::new();

    let checks: &[(&[&str], Tag)] = &[
        (
            &["security", "credential", "secret", "auth", "token", "keychain", "permission", "firewall"],
            Tag::Security,
        ),
        (
            &["performance", "latency", "throughput", "slow", "fast", "bench", "memory usage", "allocation"],
            Tag::Performance,
        ),
        (
            &["api", "endpoint", "route", "http", "rest", "graphql", "request", "response", "payload"],
            Tag::Api,
        ),
        (
            &["test", "ci", "coverage", "unit test", "integration test", "assert", "mock", "stub", "fixture"],
            Tag::Testing,
        ),
        (
            &["docs", "documentation", "readme", "wiki", "comment", "docstring", "spec", "changelog"],
            Tag::Docs,
        ),
        (
            &["dependency", "dependencies", "crate", "package", "semver", "lockfile", "cargo.toml"],
            Tag::Dependencies,
        ),
        (
            &["config", "configuration", "setting", "env", "environment", "flag", "option", ".env"],
            Tag::Config,
        ),
        (
            &["error", "errors", "result", "unwrap", "panic", "recover", "fault", "retry", "fallback"],
            Tag::Errors,
        ),
        (
            &["schema", "model", "migration", "table", "column", "database", "db", "sqlite", "relation"],
            Tag::DataModel,
        ),
    ];

    for (keywords, tag) in checks {
        if keywords.iter().any(|&kw| lower.contains(kw)) {
            tags.push(tag.clone());
        }
    }

    tags
}

// ── GFP-assisted classification ───────────────────────────────────────────────

/// Classify `text`, attempting GFP-assisted tagging with a rule-based fallback.
///
/// Routes a concise classification prompt through the `Cheap` routing lane
/// (GFP free Flash). If the lane is unavailable — no keys, quota exhausted,
/// binary missing, or any other error — the rule-based result is returned
/// transparently with no error surfaced to the caller.
///
/// # Examples
/// ```
/// # std::env::set_var("CASCADE_GFP_LANE_OFF", "1"); // hermetic doc test — no live GP call
/// use cascade_core::taxonomy::classify_topic;
/// // Works without any GFP provider configured — returns rule-based tags.
/// let tags = classify_topic("avoid using unwrap() in library code");
/// assert!(!tags.is_empty());
/// ```
pub fn classify_topic(text: &str) -> Vec<Tag> {
    // Rule-based is authoritative when GFP produces no parseable output.
    let rule_tags = classify_topic_rule_based(text);

    // Attempt GFP enhancement; fall back on any failure.
    match gfp_classify(text) {
        Some(gfp_tags) if !gfp_tags.is_empty() => gfp_tags,
        _ => rule_tags,
    }
}

/// Try to run the GFP lane classification. Returns `None` when unavailable.
fn gfp_classify(text: &str) -> Option<Vec<Tag>> {
    use crate::routing::delegate::{DelegateLane, GfpLane, LaneResult};

    let lane = GfpLane;
    // Sensitivity firewall: GfpLane is an EXTERNAL lane ("blocked for
    // Sensitive content" — delegate.rs contract). This path calls the lane
    // directly, without Router::select, so the gate MUST be applied here:
    // sensitive snippet text (PII / VA / health / financial) never leaves
    // the machine for classification — the rule-based path handles it.
    if !gfp_may_process(text, lane.provider_id()) {
        return None;
    }
    if !lane.check_availability().is_available() {
        return None;
    }

    let prompt = build_classify_prompt(text);
    let timeout = std::time::Duration::from_secs(10);

    match lane.execute(&prompt, timeout) {
        Ok(LaneResult::Output(output)) => parse_gfp_classify_response(&output),
        _ => None,
    }
}

/// Sensitivity firewall gate for the GFP classification path (pure).
///
/// Returns `true` only when the canonical `SensitivityPolicy` allows sending
/// `text` to `provider_id`. GFP (`gfp`) is untrusted for Sensitive content,
/// so any snippet the conservative classifier flags stays local.
fn gfp_may_process(text: &str, provider_id: &str) -> bool {
    use crate::sensitivity::{classify_sensitivity, SensitivityPolicy};
    SensitivityPolicy::new()
        .check(classify_sensitivity(text), provider_id)
        .is_allow()
}

fn build_classify_prompt(text: &str) -> String {
    format!(
        "Classify the following text into memory tags. \
         Return ONLY a comma-separated list of tags from: \
         decision, lesson, pattern, security, performance, api, testing, \
         docs, dependencies, config, errors, data-model. \
         Include exactly one primary tag (decision/lesson/pattern) first.\n\n\
         Text: {}\n\nTags:",
        text
    )
}

/// Parse the model's first line into tags — STRICT whitelist.
///
/// The rule-based result is authoritative whenever GFP produces no parseable
/// output, so this parser must reject junk rather than adopt it:
///   - Unknown tokens are DROPPED (never `Tag::Custom`) — a conversational
///     first line ("Sure, here are the tags: decision") must not manufacture
///     tags that override the deterministic rule-based result.
///   - The parsed set must contain at least one primary tag
///     (decision/lesson/pattern); otherwise `None` → rule-based fallback.
///     Every memory entry needs a primary tag to route to a memory file.
fn parse_gfp_classify_response(response: &str) -> Option<Vec<Tag>> {
    let line = response.lines().next()?.trim();
    let tags: Vec<Tag> = line
        .split(',')
        .map(|s| s.trim().to_lowercase())
        .filter_map(|s| string_to_tag(&s))
        .collect();
    if tags.is_empty() || !tags.iter().any(Tag::is_primary) {
        None
    } else {
        Some(tags)
    }
}

/// Map a lowercase token to a known [`Tag`] — whitelist only.
///
/// Unknown tokens return `None` (never `Tag::Custom`): the GFP prompt asks
/// for tags from a fixed vocabulary, so anything else is model junk.
fn string_to_tag(s: &str) -> Option<Tag> {
    match s {
        "decision" => Some(Tag::Decision),
        "lesson" => Some(Tag::Lesson),
        "pattern" => Some(Tag::Pattern),
        "security" => Some(Tag::Security),
        "performance" => Some(Tag::Performance),
        "api" => Some(Tag::Api),
        "testing" => Some(Tag::Testing),
        "docs" => Some(Tag::Docs),
        "dependencies" => Some(Tag::Dependencies),
        "config" => Some(Tag::Config),
        "errors" => Some(Tag::Errors),
        "data-model" => Some(Tag::DataModel),
        _ => None,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── Rule-based path ───────────────────────────────────────────────────────

    #[test]
    fn rule_based_decision() {
        let tags = classify_topic_rule_based("decided to use sqlite instead of postgres");
        assert!(tags.contains(&Tag::Decision), "expected Decision, got {:?}", tags);
    }

    #[test]
    fn rule_based_lesson() {
        let tags = classify_topic_rule_based("learned that unwrap in lib code causes panics");
        assert!(tags.contains(&Tag::Lesson), "expected Lesson, got {:?}", tags);
    }

    #[test]
    fn rule_based_pattern() {
        let tags = classify_topic_rule_based("always use the builder pattern for config structs");
        assert!(tags.contains(&Tag::Pattern), "expected Pattern, got {:?}", tags);
    }

    #[test]
    fn rule_based_security_domain() {
        let tags = classify_topic_rule_based("decided to store tokens in the OS keychain");
        assert!(tags.contains(&Tag::Security), "expected Security domain tag");
    }

    #[test]
    fn rule_based_testing_domain() {
        let tags = classify_topic_rule_based("pattern: use mock structs for all unit tests");
        assert!(tags.contains(&Tag::Testing), "expected Testing domain tag");
    }

    #[test]
    fn rule_based_returns_at_least_one_tag() {
        let tags = classify_topic_rule_based("this is some vague text without strong signals");
        assert!(!tags.is_empty(), "must always return at least one tag");
    }

    #[test]
    fn tag_display() {
        assert_eq!(Tag::Decision.to_string(), "decision");
        assert_eq!(Tag::DataModel.to_string(), "data-model");
        assert_eq!(Tag::Custom("foobar".into()).to_string(), "foobar");
    }

    #[test]
    fn tag_memory_file() {
        assert_eq!(Tag::Decision.memory_file(), Some("decisions"));
        assert_eq!(Tag::Lesson.memory_file(), Some("lessons"));
        assert_eq!(Tag::Pattern.memory_file(), Some("patterns"));
        assert_eq!(Tag::Security.memory_file(), None);
    }

    #[test]
    fn tag_is_primary() {
        assert!(Tag::Decision.is_primary());
        assert!(Tag::Lesson.is_primary());
        assert!(Tag::Pattern.is_primary());
        assert!(!Tag::Security.is_primary());
        assert!(!Tag::Api.is_primary());
    }

    // ── GFP-assisted path (no provider configured = rule-based fallback) ──────

    #[test]
    fn classify_topic_without_gfp_returns_rule_based() {
        // Hermetic: force the GFP lane off so no live proxy on the test host
        // can inject model output into this assertion.
        std::env::set_var("CASCADE_GFP_LANE_OFF", "1");
        // classify_topic() must still return a valid non-empty result.
        let tags = classify_topic("learned to avoid blocking the async runtime in tests");
        assert!(!tags.is_empty(), "must return tags even without GFP");
        let has_primary = tags.iter().any(|t| t.is_primary());
        assert!(has_primary, "must include at least one primary tag");
    }

    // ── Parse helpers ─────────────────────────────────────────────────────────

    #[test]
    fn parse_gfp_response_valid() {
        let resp = "decision, security, config\nsome extra text";
        let tags = parse_gfp_classify_response(resp).unwrap();
        assert!(tags.contains(&Tag::Decision));
        assert!(tags.contains(&Tag::Security));
        assert!(tags.contains(&Tag::Config));
    }

    #[test]
    fn parse_gfp_response_empty() {
        let tags = parse_gfp_classify_response("");
        assert!(tags.is_none());
    }

    /// Regression (E1-S6 review): unknown tokens must be DROPPED, not turned
    /// into `Tag::Custom` — model junk must never override rule-based tags.
    #[test]
    fn parse_gfp_response_unknown_tag_is_dropped() {
        let resp = "decision, somethingweird";
        let tags = parse_gfp_classify_response(resp).unwrap();
        assert_eq!(tags, vec![Tag::Decision]);
        assert!(!tags.iter().any(|t| matches!(t, Tag::Custom(_))));
    }

    /// A conversational first line with no whitelisted token parses to None
    /// so the caller falls back to the rule-based result.
    #[test]
    fn parse_gfp_response_conversational_junk_is_none() {
        assert!(parse_gfp_classify_response("Sure! Here are the tags you asked for").is_none());
    }

    /// Domain tags without a primary tag are unusable (no memory file to
    /// route to) — must fall back to rule-based.
    #[test]
    fn parse_gfp_response_without_primary_is_none() {
        assert!(parse_gfp_classify_response("security, config").is_none());
    }

    // ── Sensitivity firewall on the GFP path ──────────────────────────────────

    /// Regression (E1-S6 review): GfpLane::execute became a REAL external
    /// POST; sensitive snippet text must be gated BEFORE the lane is called.
    #[test]
    fn gfp_gate_blocks_sensitive_text() {
        assert!(!gfp_may_process("my ssn is 123-45-6789", "gfp"));
        assert!(!gfp_may_process("VA disability rating appeal notes", "gfp"));
    }

    #[test]
    fn gfp_gate_allows_public_text() {
        assert!(gfp_may_process("always use the builder idiom for structs", "gfp"));
    }

    /// End-to-end: gfp_classify must return None for sensitive text without
    /// touching the lane (no kill-switch needed — the gate fires first).
    #[test]
    fn gfp_classify_returns_none_for_sensitive_text() {
        // Deliberately do NOT rely on CASCADE_GFP_LANE_OFF here: even with a
        // live proxy on this host, the sensitivity gate must short-circuit.
        assert!(gfp_classify("my ssn is 123-45-6789, decided to rotate it").is_none());
    }
}
