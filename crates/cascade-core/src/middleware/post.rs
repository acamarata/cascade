//! # middleware::post — response POST-processing digest (E2-S3)
//!
//! ## Purpose
//!
//! Pure extraction of a structured [`ResponseDigest`] from an assistant
//! response string so the daemon can sync conversation outcomes (decisions,
//! files touched, completed tasks) into `~/.cascade/context-sync/` AFTER the
//! response has been delivered. Everything here is hot-path-free by design:
//! the daemon only calls these functions from a background task spawned once
//! the final SSE sentinel has been sent, and only when the
//! `middleware.context_sync` flag is on (default OFF).
//!
//! ## Inputs / Outputs
//!
//! - Input: the full assistant response text plus an injected
//!   `path_exists` closure (so tests never need a real filesystem) and an
//!   injected `classify` closure for the taxonomy hook.
//! - Output: a typed [`ResponseDigest`] and its JSONL serialisation via
//!   [`to_jsonl_line`] (timestamp injected by the caller — this module stays
//!   pure: no I/O, no clock reads).
//!
//! ## Taxonomy hook
//!
//! [`extract_digest`] uses the deterministic
//! [`crate::taxonomy::classify_topic_rule_based`] path (pure, always
//! available). [`extract_digest_taxonomy`] upgrades to
//! [`crate::taxonomy::classify_topic`], which attempts the GFP lane and
//! transparently falls back to the rule-based result on ANY provider failure
//! — post-processing never hard-depends on GP. That variant may block on a
//! curl subprocess, so async callers must wrap it in `spawn_blocking`.
//!
//! ## Constraints
//!
//! - All extraction functions are pure (no I/O, no clock reads) and
//!   unit-tested without network or filesystem.
//! - `path_exists` is the ONLY authority on which path-like tokens count as
//!   files touched — URLs and prose like "and/or" are rejected by that gate.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: middleware::post (E2-S3)

use once_cell::sync::Lazy;
use regex::Regex;
use serde::{Deserialize, Serialize};

use crate::taxonomy::{classify_topic, classify_topic_rule_based, Tag};

// ── ResponseDigest ─────────────────────────────────────────────────────────────

/// Structured digest of one assistant response (E2-S3 context sync).
///
/// Serialised as one JSONL entry per response via [`to_jsonl_line`].
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ResponseDigest {
    /// Lines that state a decision ("decided to…", "going with…", …).
    pub decisions: Vec<String>,
    /// Path-like tokens that passed the injected existence check.
    pub files_touched: Vec<String>,
    /// Completed-task markers ("- [x] …", "Done: …", "✅ …").
    pub completed_tasks: Vec<String>,
    /// Taxonomy tags for the response text (rule-based or GFP-assisted).
    pub tags: Vec<String>,
}

impl ResponseDigest {
    /// `true` when the response carried no syncable signal.
    ///
    /// Tags alone do NOT count: the taxonomy always produces a primary tag
    /// even for vague text, so a tags-only digest would be pure noise. The
    /// daemon skips the JSONL append for empty digests.
    pub fn is_empty(&self) -> bool {
        self.decisions.is_empty()
            && self.files_touched.is_empty()
            && self.completed_tasks.is_empty()
    }
}

// ── Decision extraction ────────────────────────────────────────────────────────

/// Line-level signals that a sentence states a decision.
///
/// Deliberately overlaps the [`crate::taxonomy`] decision keyword set but is
/// applied PER LINE (the taxonomy classifies whole snippets): a digest wants
/// the individual decision sentences, not one label for the response.
const DECISION_LINE_SIGNALS: &[&str] = &[
    "decided",
    "decision:",
    "chose ",
    "chosen ",
    "going with",
    "opted",
    "switched to",
    "settled on",
    "we will use",
    "we'll use",
];

/// Extract decision-stating lines from a response (pure).
///
/// A line qualifies when its lowercase form contains any
/// [`DECISION_LINE_SIGNALS`] entry. Lines are trimmed (leading markdown
/// bullets stripped) and deduplicated preserving first-seen order.
pub fn extract_decisions(response: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for line in response.lines() {
        let trimmed = strip_bullet(line.trim());
        if trimmed.is_empty() {
            continue;
        }
        let lower = trimmed.to_lowercase();
        if DECISION_LINE_SIGNALS.iter().any(|sig| lower.contains(sig)) {
            push_unique(&mut out, trimmed.to_string());
        }
    }
    out
}

// ── File-path extraction ───────────────────────────────────────────────────────

/// Path-like token: optional first segment then one or more `/segment` parts.
///
/// Matches `src/lib.rs`, `crates/foo/src/bar.rs`, `/abs/path.md`,
/// `.claude/memory/notes.md`. Colons are excluded from the class so URL
/// schemes never join a token; the leftover `//host/path` shapes are then
/// rejected by the existence gate.
static PATH_TOKEN_RE: Lazy<Regex> = Lazy::new(|| {
    // Static pattern — cannot fail to compile (verified by tests).
    #[allow(clippy::expect_used)]
    Regex::new(r"[A-Za-z0-9_@~.-]*(?:/[A-Za-z0-9_@.-]+)+").expect("static path regex")
});

/// Extract candidate path-like tokens from a response (pure, no FS access).
///
/// Trailing sentence punctuation (`.,:;`) is stripped so "see `src/foo.rs`."
/// yields `src/foo.rs`. Deduplicated preserving first-seen order.
pub fn extract_path_candidates(response: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for m in PATH_TOKEN_RE.find_iter(response) {
        let token = m
            .as_str()
            .trim_end_matches(['.', ',', ':', ';'])
            .to_string();
        // A token must still contain a separator after trimming.
        if token.contains('/') {
            push_unique(&mut out, token);
        }
    }
    out
}

/// Extract the file paths a response touched (pure — FS check is injected).
///
/// `path_exists` receives each candidate token and returns whether it exists
/// under the caller's project root; the closure is the ONLY existence
/// authority so tests run without a real filesystem.
pub fn extract_files_touched(
    response: &str,
    path_exists: impl Fn(&str) -> bool,
) -> Vec<String> {
    extract_path_candidates(response)
        .into_iter()
        .filter(|p| path_exists(p))
        .collect()
}

// ── Completed-task extraction ──────────────────────────────────────────────────

/// Extract completed-task markers from a response (pure).
///
/// Recognised shapes (leading bullet/whitespace ignored):
/// - checked checkbox: `- [x] task` / `* [X] task`
/// - label lines: `Done: task` / `Completed: task` (case-insensitive)
/// - check-marked lines: `✅ task` / `✓ task`
pub fn extract_completed_tasks(response: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for line in response.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            continue;
        }
        if let Some(task) = completed_task_text(trimmed) {
            if !task.is_empty() {
                push_unique(&mut out, task);
            }
        }
    }
    out
}

/// Parse one trimmed line into its completed-task text, if any (pure).
fn completed_task_text(trimmed: &str) -> Option<String> {
    // Checked checkbox after a `-` or `*` bullet.
    let after_bullet = trimmed
        .strip_prefix("- ")
        .or_else(|| trimmed.strip_prefix("* "))
        .unwrap_or(trimmed);
    for check in ["[x] ", "[X] "] {
        if let Some(rest) = after_bullet.strip_prefix(check) {
            return Some(rest.trim().to_string());
        }
    }
    // Label lines.
    let lower = after_bullet.to_lowercase();
    for label in ["done:", "completed:"] {
        if lower.starts_with(label) {
            return Some(after_bullet[label.len()..].trim().to_string());
        }
    }
    // Check-mark prefixed lines.
    for mark in ['✅', '✓'] {
        if let Some(rest) = after_bullet.strip_prefix(mark) {
            return Some(rest.trim().to_string());
        }
    }
    None
}

// ── Secret redaction ───────────────────────────────────────────────────────────

/// Bare secret-token shapes: well-known key/token prefixes plus JWTs.
///
/// Digest lines are copied VERBATIM from assistant text into the context-sync
/// JSONL and from there into the global RAG index, so any echoed secret must
/// be scrubbed BEFORE it is stored. Shapes covered: Anthropic/OpenAI `sk-…`,
/// GitHub `ghp_`/`gho_`/`github_pat_`, AWS `AKIA…`, Google `AIza…`, Slack
/// `xox?-…`, and three-part JWTs.
static SECRET_TOKEN_RE: Lazy<Regex> = Lazy::new(|| {
    // Static pattern — cannot fail to compile (verified by tests).
    #[allow(clippy::expect_used)]
    Regex::new(
        r"(?x)
          sk-[A-Za-z0-9_-]{8,}
        | (?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{16,}
        | github_pat_[A-Za-z0-9_]{20,}
        | AKIA[0-9A-Z]{16}
        | AIza[0-9A-Za-z_-]{30,}
        | xox[baprs]-[A-Za-z0-9-]{10,}
        | eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}
        ",
    )
    .expect("static secret-token regex")
});

/// Credential assignments: `SOME_KEY=value`, `token: value`, `password=…`.
///
/// Keeps the variable/label name (useful context) and redacts only the value.
static SECRET_ASSIGNMENT_RE: Lazy<Regex> = Lazy::new(|| {
    // Static pattern — cannot fail to compile (verified by tests).
    #[allow(clippy::expect_used)]
    Regex::new(
        r#"(?i)\b([A-Za-z0-9_.-]*(?:key|token|secret|password|passwd|credential)s?[A-Za-z0-9_.-]*\s*[:=]\s*)("[^"]+"|'[^']+'|\S+)"#,
    )
    .expect("static secret-assignment regex")
});

/// Replacement marker for scrubbed secret material.
pub const REDACTED: &str = "[REDACTED]";

/// Scrub secret material from one digest line (pure).
///
/// Two passes: credential assignments first (label kept, value replaced),
/// then bare token shapes. Lines without secrets pass through byte-identical.
pub fn redact_secrets(line: &str) -> String {
    let pass1 = SECRET_ASSIGNMENT_RE.replace_all(line, format!("${{1}}{REDACTED}"));
    SECRET_TOKEN_RE.replace_all(&pass1, REDACTED).into_owned()
}

// ── Digest orchestrators ───────────────────────────────────────────────────────

/// Digest extraction with an injected taxonomy classifier (the unit-testable
/// core — no network, no FS beyond the injected closure).
///
/// `classify` is called AT MOST ONCE, and only when the pure extractors found
/// at least one signal — empty digests never trigger classification.
pub fn extract_digest_with(
    response: &str,
    path_exists: impl Fn(&str) -> bool,
    classify: impl FnOnce(&str) -> Vec<Tag>,
) -> ResponseDigest {
    // Decision/task lines are verbatim copies of assistant text destined for
    // the context-sync JSONL (and from there the RAG index) — scrub echoed
    // secrets before they are stored anywhere. files_touched are existence-
    // gated path tokens, not free text, so they need no scrubbing.
    let mut digest = ResponseDigest {
        decisions: extract_decisions(response)
            .into_iter()
            .map(|l| redact_secrets(&l))
            .collect(),
        files_touched: extract_files_touched(response, path_exists),
        completed_tasks: extract_completed_tasks(response)
            .into_iter()
            .map(|l| redact_secrets(&l))
            .collect(),
        tags: Vec::new(),
    };
    if !digest.is_empty() {
        let mut tags: Vec<String> = Vec::new();
        for tag in classify(response) {
            push_unique(&mut tags, tag.to_string());
        }
        digest.tags = tags;
    }
    digest
}

/// Fully pure digest extraction: rule-based taxonomy only (no I/O of any kind
/// beyond the injected `path_exists` closure).
pub fn extract_digest(
    response: &str,
    path_exists: impl Fn(&str) -> bool,
) -> ResponseDigest {
    extract_digest_with(response, path_exists, classify_topic_rule_based)
}

/// Digest extraction with the GFP-assisted taxonomy hook (E2-S3 item 3).
///
/// [`classify_topic`] attempts the GFP `Cheap` lane and degrades to the
/// rule-based result on ANY failure (no keys, proxy down, sensitive content
/// gate) — GP is never a hard dependency for post-processing. MAY BLOCK on a
/// bounded curl subprocess; async callers must use `spawn_blocking`. Intended
/// for the daemon's background sync task only — never the hot path.
pub fn extract_digest_taxonomy(
    response: &str,
    path_exists: impl Fn(&str) -> bool,
) -> ResponseDigest {
    extract_digest_with(response, path_exists, classify_topic)
}

// ── JSONL serialisation ────────────────────────────────────────────────────────

/// Serialise a digest as one JSONL line with an injected timestamp (pure).
///
/// The timestamp is a caller-supplied RFC 3339 string so this module never
/// reads the clock. Returns the JSON object WITHOUT a trailing newline — the
/// appender adds it so a line is always written atomically as one unit.
pub fn to_jsonl_line(digest: &ResponseDigest, ts_rfc3339: &str) -> String {
    serde_json::json!({
        "ts": ts_rfc3339,
        "decisions": digest.decisions,
        "files_touched": digest.files_touched,
        "completed_tasks": digest.completed_tasks,
        "tags": digest.tags,
    })
    .to_string()
}

// ── Small helpers ──────────────────────────────────────────────────────────────

/// Strip one leading markdown bullet (`- ` / `* `) from a trimmed line.
fn strip_bullet(trimmed: &str) -> &str {
    trimmed
        .strip_prefix("- ")
        .or_else(|| trimmed.strip_prefix("* "))
        .unwrap_or(trimmed)
        .trim()
}

/// Push preserving first-seen order, skipping duplicates.
fn push_unique(out: &mut Vec<String>, value: String) {
    if !out.contains(&value) {
        out.push(value);
    }
}

// ── Tests (pure — no network, no real FS) ──────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    const FIXTURE: &str = "\
I looked at the repo and made changes.

Decided to use sqlite for the store instead of postgres.
- We are going with tokio for the runtime.
Edited src/lib.rs and crates/core/src/store.rs to add the module.
See https://example.com/docs for background.

- [x] wire the store into the daemon
* [X] add unit tests
Done: update the changelog
✅ verified the build
✓ ran clippy
";

    fn fixture_exists(p: &str) -> bool {
        matches!(p, "src/lib.rs" | "crates/core/src/store.rs")
    }

    // ── decisions ─────────────────────────────────────────────────────────────

    #[test]
    fn decisions_extracted_from_fixture() {
        let d = extract_decisions(FIXTURE);
        assert_eq!(d.len(), 2, "got: {d:?}");
        assert!(d[0].starts_with("Decided to use sqlite"));
        assert!(d[1].starts_with("We are going with tokio"));
    }

    #[test]
    fn decisions_none_found_is_empty() {
        assert!(extract_decisions("just some prose about nothing").is_empty());
        assert!(extract_decisions("").is_empty());
    }

    #[test]
    fn decisions_deduplicated() {
        let text = "decided to ship it\ndecided to ship it\n";
        assert_eq!(extract_decisions(text).len(), 1);
    }

    // ── path candidates + existence closure injection ─────────────────────────

    #[test]
    fn path_candidates_found_in_fixture() {
        let c = extract_path_candidates(FIXTURE);
        assert!(c.contains(&"src/lib.rs".to_string()), "got: {c:?}");
        assert!(c.contains(&"crates/core/src/store.rs".to_string()), "got: {c:?}");
    }

    #[test]
    fn trailing_punctuation_stripped_from_paths() {
        let c = extract_path_candidates("edited src/foo.rs, then src/bar.rs.");
        assert!(c.contains(&"src/foo.rs".to_string()), "got: {c:?}");
        assert!(c.contains(&"src/bar.rs".to_string()), "got: {c:?}");
    }

    #[test]
    fn files_touched_honours_injected_closure() {
        // The closure is the ONLY existence authority.
        let all = extract_files_touched(FIXTURE, |_| true);
        assert!(all.len() >= 2, "got: {all:?}");

        let none = extract_files_touched(FIXTURE, |_| false);
        assert!(none.is_empty(), "closure=false must reject all: {none:?}");

        let some = extract_files_touched(FIXTURE, fixture_exists);
        assert_eq!(
            some,
            vec!["src/lib.rs".to_string(), "crates/core/src/store.rs".to_string()]
        );
    }

    #[test]
    fn url_fragments_rejected_by_existence_gate() {
        let got = extract_files_touched(
            "see https://example.com/docs/page.html for details",
            fixture_exists,
        );
        assert!(got.is_empty(), "URL fragments must not survive the gate: {got:?}");
    }

    // ── completed tasks ───────────────────────────────────────────────────────

    #[test]
    fn completed_tasks_all_marker_shapes() {
        let t = extract_completed_tasks(FIXTURE);
        assert_eq!(
            t,
            vec![
                "wire the store into the daemon".to_string(),
                "add unit tests".to_string(),
                "update the changelog".to_string(),
                "verified the build".to_string(),
                "ran clippy".to_string(),
            ]
        );
    }

    #[test]
    fn unchecked_checkbox_is_not_completed() {
        let t = extract_completed_tasks("- [ ] still open\n- [x] closed");
        assert_eq!(t, vec!["closed".to_string()]);
    }

    #[test]
    fn completed_tasks_none_found_is_empty() {
        assert!(extract_completed_tasks("no tasks here").is_empty());
    }

    // ── digest orchestrator ───────────────────────────────────────────────────

    #[test]
    fn digest_full_fixture() {
        let d = extract_digest(FIXTURE, fixture_exists);
        assert_eq!(d.decisions.len(), 2);
        assert_eq!(d.files_touched.len(), 2);
        assert_eq!(d.completed_tasks.len(), 5);
        assert!(!d.is_empty());
        // Rule-based taxonomy ran (fixture contains decision signals).
        assert!(d.tags.contains(&"decision".to_string()), "tags: {:?}", d.tags);
    }

    #[test]
    fn digest_none_found_is_empty_and_skips_classify() {
        let d = extract_digest_with(
            "plain prose, nothing actionable",
            |_| false,
            |_| panic!("classify must not be called for an empty digest"),
        );
        assert!(d.is_empty());
        assert!(d.tags.is_empty());
    }

    #[test]
    fn digest_with_injected_classifier_maps_tags() {
        let d = extract_digest_with(FIXTURE, fixture_exists, |_| {
            vec![Tag::Decision, Tag::Testing, Tag::Decision]
        });
        // Deduplicated, Display-mapped.
        assert_eq!(d.tags, vec!["decision".to_string(), "testing".to_string()]);
    }

    #[test]
    fn digest_is_empty_ignores_tags() {
        let d = ResponseDigest {
            tags: vec!["decision".into()],
            ..Default::default()
        };
        assert!(d.is_empty(), "tags alone must not make a digest syncable");
    }

    // ── secret redaction ──────────────────────────────────────────────────────

    #[test]
    fn redact_assignment_keeps_label_scrubs_value() {
        let got = redact_secrets("Decided to set ANTHROPIC_API_KEY=sk-ant-api03-abc123 in env");
        assert!(got.contains("ANTHROPIC_API_KEY="), "label kept: {got}");
        assert!(!got.contains("sk-ant-"), "value scrubbed: {got}");
        assert!(got.contains(REDACTED), "marker present: {got}");
    }

    #[test]
    fn redact_bare_token_shapes() {
        for line in [
            "Done: rotated key ghp_abcdefghijklmnopqrstuvwx",
            "went with AKIAIOSFODNN7EXAMPLE for now",
            "AIzaSyA1234567890abcdefghijklmnopqrstuv works",
            "xoxb-123456789012-abcdefghij is live",
        ] {
            let got = redact_secrets(line);
            assert!(got.contains(REDACTED), "input: {line} → {got}");
        }
    }

    #[test]
    fn redact_clean_line_is_identity() {
        let line = "Decided to use sqlite for the store instead of postgres.";
        assert_eq!(redact_secrets(line), line);
    }

    /// End-to-end: an echoed secret in a decision/task line never reaches the
    /// digest (the JSONL/RAG sink) — the exact failure traced in review.
    #[test]
    fn digest_lines_are_secret_redacted() {
        let text = "\
Decided to set ANTHROPIC_API_KEY=sk-ant-api03-secret123 for the daemon.
Done: rotated key ghp_abcdefghijklmnopqrstuvwx and pushed.
";
        let d = extract_digest(text, |_| false);
        assert_eq!(d.decisions.len(), 1);
        assert_eq!(d.completed_tasks.len(), 1);
        let line = to_jsonl_line(&d, "2026-07-03T00:00:00Z");
        assert!(!line.contains("sk-ant-"), "jsonl: {line}");
        assert!(!line.contains("ghp_"), "jsonl: {line}");
        assert!(line.contains(REDACTED), "jsonl: {line}");
    }

    // ── JSONL serialisation ───────────────────────────────────────────────────

    #[test]
    fn jsonl_line_round_trips() {
        let d = extract_digest(FIXTURE, fixture_exists);
        let line = to_jsonl_line(&d, "2026-07-03T00:00:00Z");
        assert!(!line.contains('\n'), "one line, no embedded newlines");

        let v: serde_json::Value = serde_json::from_str(&line).expect("valid JSON");
        assert_eq!(v["ts"], "2026-07-03T00:00:00Z");
        assert_eq!(v["decisions"].as_array().map(Vec::len), Some(2));
        assert_eq!(v["files_touched"].as_array().map(Vec::len), Some(2));
        assert_eq!(v["completed_tasks"].as_array().map(Vec::len), Some(5));
        assert_eq!(v["files_touched"][0], "src/lib.rs");
    }

    #[test]
    fn jsonl_line_empty_digest_shape() {
        let line = to_jsonl_line(&ResponseDigest::default(), "2026-07-03T01:02:03Z");
        let v: serde_json::Value = serde_json::from_str(&line).expect("valid JSON");
        assert_eq!(v["decisions"].as_array().map(Vec::len), Some(0));
        assert_eq!(v["tags"].as_array().map(Vec::len), Some(0));
    }
}
