//! # middleware::pre — GP-powered PRE-processing (E2-S2)
//!
//! ## Purpose
//!
//! Three independent, flag-gated pre-dispatch transforms:
//!
//! 1. **Context compression** — when the estimated token count of a message
//!    list exceeds a threshold, keep the last [`KEEP_LAST_TURNS`] turns
//!    verbatim and replace all older turns with ONE summary block produced by
//!    a single bounded GP call. On ANY GP failure the ORIGINAL messages are
//!    returned unchanged.
//! 2. **System-prompt injection** — a pure template merge of optional project
//!    context strings into one provider-neutral system-prefix block.
//! 3. **Request classification** — one bounded GP call with a strict
//!    one-word expected reply, leniently parsed to Trivial/Medium/Complex and
//!    mapped onto the selection module's [`Tier`].
//!
//! ## Inputs / Outputs
//!
//! Pure functions take/return owned or borrowed [`ChatTurn`] slices and
//! strings; the two `*_via_gp` / `classify_prompt` adapters are the ONLY I/O
//! points and both go through [`crate::routing::gfp_http::post_messages`]
//! (the proven :3762 curl transport — no new HTTP client).
//!
//! ## Constraints
//!
//! - GP calls are bounded ([`GP_SUMMARY_TIMEOUT`] / [`GP_CLASSIFY_TIMEOUT`])
//!   and synchronous (curl subprocess); async callers must wrap them in
//!   `spawn_blocking`.
//! - Never degrade correctness for savings: any failure → original input
//!   (compression) or `None` (classification).
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: middleware::pre (E2-S2)

use std::time::Duration;

use crate::routing::delegate::LaneResult;
use crate::routing::gfp_http::{post_messages, GFP_MESSAGES_URL};
use crate::selection::Tier;

// ── Constants ──────────────────────────────────────────────────────────────────

/// Token-estimate heuristic: ~4 characters per token.
///
/// This is the industry-standard rough estimate for English/code text with
/// BPE-family tokenizers (GPT/Claude/Gemini all land near 3.5–4.5 chars per
/// token). It deliberately errs simple: compression is a savings trigger,
/// not an accounting surface, so an exact tokenizer dependency is not worth
/// the build cost.
pub const CHARS_PER_TOKEN: usize = 4;

/// How many most-recent turns are ALWAYS kept verbatim by compression.
///
/// Six turns ≈ three user/assistant exchanges — enough for the model to keep
/// short-range anaphora ("do that again", "same as above") intact while the
/// older tail gets summarised.
pub const KEEP_LAST_TURNS: usize = 6;

/// Default compression trigger: estimated tokens above this → compress.
///
/// 8k tokens (~32 KiB of text) is well under every supported provider's
/// context window but large enough that the GP summary call (plus its risk
/// of losing nuance in old turns) only happens for genuinely long chats.
pub const DEFAULT_COMPRESS_THRESHOLD_TOKENS: usize = 8_000;

/// Upper bound on the GP summarisation call (spec: bounded ~30 s).
pub const GP_SUMMARY_TIMEOUT: Duration = Duration::from_secs(30);

/// Upper bound on the GP classification call — the expected reply is a
/// single word, so a much tighter budget applies.
pub const GP_CLASSIFY_TIMEOUT: Duration = Duration::from_secs(10);

// ── Shared types ───────────────────────────────────────────────────────────────

/// One conversation turn — the middleware's provider-neutral message shape.
///
/// Deliberately NOT `cascade_providers::Message`: cascade-core carries no
/// dependency on the providers crate; callers convert at the boundary.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChatTurn {
    /// Role string as used by the chat surface ("user"/"assistant"/"system").
    pub role: String,
    /// Turn text content.
    pub content: String,
}

impl ChatTurn {
    /// Convenience constructor.
    pub fn new(role: impl Into<String>, content: impl Into<String>) -> Self {
        Self { role: role.into(), content: content.into() }
    }
}

// ── 1. Context compression — pure splitter/assembler ──────────────────────────

/// Estimate the token count of a message list via the chars/4 heuristic.
///
/// Pure. Counts `content` characters only (role labels are negligible).
pub fn estimate_tokens(turns: &[ChatTurn]) -> usize {
    let chars: usize = turns.iter().map(|t| t.content.chars().count()).sum();
    chars / CHARS_PER_TOKEN
}

/// Split a message list into `(older, recent)` where `recent` is the last
/// `keep_last` turns kept verbatim and `older` is everything before them.
///
/// Returns `None` when there is nothing to compress — i.e. the list has at
/// most `keep_last` turns (the whole conversation is already "recent").
/// Pure; borrows sub-slices, copies nothing.
pub fn split_for_compression(
    turns: &[ChatTurn],
    keep_last: usize,
) -> Option<(&[ChatTurn], &[ChatTurn])> {
    if turns.len() <= keep_last {
        return None;
    }
    Some(turns.split_at(turns.len() - keep_last))
}

/// Build the GP summarisation prompt for the older turns (pure).
///
/// The instruction asks for a dense factual summary — decisions, facts,
/// constraints, open questions — because the summary replaces real history:
/// anything the summary drops is gone for the downstream model.
pub fn summary_prompt(older: &[ChatTurn]) -> String {
    let mut transcript = String::new();
    for t in older {
        transcript.push_str(&t.role);
        transcript.push_str(": ");
        transcript.push_str(&t.content);
        transcript.push('\n');
    }
    format!(
        "Summarize the following conversation history into one dense paragraph. \
         Preserve every decision, fact, constraint, name, and open question. \
         Do not add commentary or formatting. Output only the summary.\n\n{transcript}"
    )
}

/// Assemble the compressed message list: one summary block followed by the
/// recent turns verbatim (pure).
///
/// The summary travels as a `system` turn so every provider treats it as
/// context rather than as a user utterance to answer.
pub fn assemble_compressed(summary: &str, recent: &[ChatTurn]) -> Vec<ChatTurn> {
    let mut out = Vec::with_capacity(recent.len() + 1);
    out.push(ChatTurn::new(
        "system",
        format!("Summary of the earlier conversation (older turns compressed):\n{summary}"),
    ));
    out.extend(recent.iter().cloned());
    out
}

/// Compression orchestrator with an injected summariser (the unit-testable
/// core — no network).
///
/// Inputs:  `turns` — full message list. `threshold_tokens` — compress only
///          when [`estimate_tokens`] exceeds this. `keep_last` — verbatim
///          tail size. `summarize` — called AT MOST ONCE with the summary
///          prompt; `None` signals failure.
/// Outputs: the compressed list, or a clone of the ORIGINAL list whenever
///          compression does not apply or the summariser fails. The quality
///          guard is absolute: no partial/degraded output is ever returned.
pub fn compress_context_with(
    turns: &[ChatTurn],
    threshold_tokens: usize,
    keep_last: usize,
    summarize: impl FnOnce(&str) -> Option<String>,
) -> Vec<ChatTurn> {
    if estimate_tokens(turns) <= threshold_tokens {
        return turns.to_vec();
    }
    let Some((older, recent)) = split_for_compression(turns, keep_last) else {
        return turns.to_vec();
    };
    match summarize(&summary_prompt(older)) {
        Some(summary) if !summary.trim().is_empty() => {
            assemble_compressed(summary.trim(), recent)
        }
        // GP failure or empty summary → original messages, unchanged.
        _ => turns.to_vec(),
    }
}

/// Production compression: [`compress_context_with`] wired to ONE bounded GP
/// call through the existing gfp_http transport.
///
/// Blocking (curl subprocess) — async callers must use `spawn_blocking`.
/// Uses [`DEFAULT_COMPRESS_THRESHOLD_TOKENS`] and [`KEEP_LAST_TURNS`].
pub fn compress_context_via_gp(turns: &[ChatTurn]) -> Vec<ChatTurn> {
    compress_context_with(turns, DEFAULT_COMPRESS_THRESHOLD_TOKENS, KEEP_LAST_TURNS, |prompt| {
        gp_complete(prompt, GP_SUMMARY_TIMEOUT)
    })
}

// ── 2. System-prompt injection — pure template merge ──────────────────────────

/// Optional project context strings supplied BY THE CALLER (no I/O here).
///
/// The daemon (or any other surface) decides where these strings come from;
/// this module only merges them.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ProjectContext {
    /// Condensed CLAUDE.md / CASCADE.md project instructions.
    pub claude_md_summary: Option<String>,
    /// The currently active task, if any.
    pub active_task: Option<String>,
    /// Stack/tooling notes (frameworks, package manager, conventions).
    pub stack_notes: Option<String>,
}

/// Merge project context into one provider-neutral system-prefix block (pure).
///
/// Returns `None` when every field is absent or blank — callers then leave
/// the request's system prompt untouched.
///
/// STABLE OUTPUT — cache-prefix-safe by construction: field order is fixed
/// (summary → task → notes), there are no timestamps, counters, or any other
/// time-varying content, and identical inputs produce identical bytes. WHY:
/// providers cache prompt prefixes by exact byte match (e.g. Anthropic
/// `cache_control`, Gemini implicit caching); a single varying byte at the
/// front of the system prompt would invalidate the cache on every request
/// and turn a savings feature into a cost multiplier.
pub fn build_system_prefix(ctx: &ProjectContext) -> Option<String> {
    // Fixed (label, value) order — never reordered, never timestamped.
    let sections: [(&str, &Option<String>); 3] = [
        ("Project instructions (summary)", &ctx.claude_md_summary),
        ("Active task", &ctx.active_task),
        ("Stack notes", &ctx.stack_notes),
    ];

    let mut out = String::new();
    for (label, value) in sections {
        if let Some(v) = value {
            let v = v.trim();
            if !v.is_empty() {
                out.push_str("## ");
                out.push_str(label);
                out.push('\n');
                out.push_str(v);
                out.push_str("\n\n");
            }
        }
    }
    if out.is_empty() {
        None
    } else {
        Some(format!("# Project context\n\n{}", out.trim_end()))
    }
}

// ── 3. Request classification ──────────────────────────────────────────────────

/// Coarse complexity of an incoming request.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RequestComplexity {
    /// Lookup/summarize/classify-grade work.
    Trivial,
    /// Ordinary code/writing/analysis work.
    Medium,
    /// Multi-step reasoning, architecture, adversarial work.
    Complex,
}

/// Build the strict one-word classification prompt (pure).
///
/// The expected reply is a single word (≈3 tokens with punctuation slack),
/// which keeps the GP call cheap and the parse unambiguous.
pub fn classification_prompt(user_prompt: &str) -> String {
    format!(
        "Classify the difficulty of the following request. \
         Reply with exactly one word: trivial, medium, or complex. \
         No punctuation, no explanation.\n\nRequest:\n{user_prompt}"
    )
}

/// Leniently parse a classification reply (pure).
///
/// Case-insensitive substring scan; when several labels appear the EARLIEST
/// occurrence wins (models sometimes echo alternatives before settling).
/// Empty or unrecognisable replies → `None` — the caller keeps its own tier.
pub fn parse_classification(reply: &str) -> Option<RequestComplexity> {
    let lower = reply.to_lowercase();
    let candidates = [
        ("trivial", RequestComplexity::Trivial),
        ("medium", RequestComplexity::Medium),
        ("complex", RequestComplexity::Complex),
    ];
    candidates
        .iter()
        .filter_map(|(word, c)| lower.find(word).map(|pos| (pos, *c)))
        .min_by_key(|(pos, _)| *pos)
        .map(|(_, c)| c)
}

/// Classification orchestrator with an injected completion fn (unit-testable
/// core — no network). `complete` is called AT MOST ONCE with the
/// classification prompt; `None` signals failure.
pub fn classify_with(
    user_prompt: &str,
    complete: impl FnOnce(&str) -> Option<String>,
) -> Option<RequestComplexity> {
    complete(&classification_prompt(user_prompt)).and_then(|reply| parse_classification(&reply))
}

/// Production classification: ONE bounded GP call via the gfp_http transport.
///
/// Returns `None` on ANY failure (proxy down, timeout, garbage reply) — the
/// caller keeps its own tier/model choice. Blocking (curl subprocess);
/// async callers must use `spawn_blocking`.
pub fn classify_prompt(user_prompt: &str) -> Option<RequestComplexity> {
    classify_with(user_prompt, |prompt| gp_complete(prompt, GP_CLASSIFY_TIMEOUT))
}

/// Map a classification onto the selection module's [`Tier`] (pure).
///
/// | Complexity | Tier |
/// |---|---|
/// | Trivial | T3 |
/// | Medium | T2 |
/// | Complex | T1 |
pub fn tier_for_complexity(c: RequestComplexity) -> Tier {
    match c {
        RequestComplexity::Trivial => Tier::T3,
        RequestComplexity::Medium => Tier::T2,
        RequestComplexity::Complex => Tier::T1,
    }
}

// ── GP transport adapter (the only I/O in this module) ────────────────────────

/// Sensitivity-firewall gate for middleware GP calls (pure).
///
/// The compression summary prompt and the classification prompt both embed
/// conversation content VERBATIM, so before either leaves the machine the
/// canonical [`crate::sensitivity::SensitivityPolicy`] must approve sending
/// that text to the GFP provider (external — untrusted for Sensitive
/// content). Mirrors `taxonomy::gfp_may_process`: sensitive text (PII / VA /
/// health / financial / credentials) never reaches Google — the caller falls
/// back to its no-GP behaviour (original messages / no classification).
pub fn gp_may_process(prompt: &str) -> bool {
    use crate::sensitivity::{classify_sensitivity, SensitivityPolicy};
    SensitivityPolicy::new()
        .check(
            classify_sensitivity(prompt),
            cascade_types::quota_store::PROVIDER_GFP,
        )
        .is_allow()
}

/// One bounded completion via the existing GP HTTP lane; `None` on any
/// failure. Reuses [`post_messages`] — the proven curl transport to the
/// :3762 Anthropic-compat proxy — rather than introducing a new HTTP client.
///
/// Two hard gates run BEFORE any network I/O:
/// 1. `CASCADE_GFP_LANE_OFF` — the same operational kill switch honoured by
///    `routing::delegate::GfpLane`; middleware GP calls must not outlive it.
/// 2. [`gp_may_process`] — the sensitivity firewall; sensitive prompt content
///    is never posted to the (external) GP pool.
fn gp_complete(prompt: &str, timeout: Duration) -> Option<String> {
    if std::env::var_os("CASCADE_GFP_LANE_OFF").is_some() {
        return None;
    }
    if !gp_may_process(prompt) {
        return None;
    }
    match post_messages(GFP_MESSAGES_URL, prompt, timeout) {
        Ok(LaneResult::Output(text)) => Some(text),
        // Unavailable (proxy down / pool exhausted / timeout / unparseable)
        // and subprocess I/O errors are all "no answer" — never fabricated.
        Ok(LaneResult::Unavailable(_)) | Err(_) => None,
    }
}

// ── Tests (pure — no network) ──────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn turn(role: &str, content: &str) -> ChatTurn {
        ChatTurn::new(role, content)
    }

    fn convo(n: usize) -> Vec<ChatTurn> {
        (0..n)
            .map(|i| {
                let role = if i % 2 == 0 { "user" } else { "assistant" };
                turn(role, &format!("turn {i}"))
            })
            .collect()
    }

    // ── estimate_tokens ───────────────────────────────────────────────────────

    #[test]
    fn estimate_tokens_is_chars_over_four() {
        let turns = vec![turn("user", "abcd"), turn("assistant", "efghijkl")];
        assert_eq!(estimate_tokens(&turns), 3); // (4 + 8) / 4
        assert_eq!(estimate_tokens(&[]), 0);
    }

    // ── splitter ──────────────────────────────────────────────────────────────

    #[test]
    fn split_keeps_last_n_verbatim() {
        let turns = convo(10);
        let (older, recent) = split_for_compression(&turns, 6).expect("split");
        assert_eq!(older.len(), 4);
        assert_eq!(recent.len(), 6);
        assert_eq!(recent, &turns[4..]);
        assert_eq!(older, &turns[..4]);
    }

    #[test]
    fn split_boundary_len_equal_keep_is_none() {
        let turns = convo(6);
        assert!(split_for_compression(&turns, 6).is_none());
    }

    #[test]
    fn split_boundary_len_one_over_keep() {
        let turns = convo(7);
        let (older, recent) = split_for_compression(&turns, 6).expect("split");
        assert_eq!(older.len(), 1);
        assert_eq!(recent.len(), 6);
    }

    #[test]
    fn split_empty_and_short_lists_are_none() {
        assert!(split_for_compression(&[], 6).is_none());
        assert!(split_for_compression(&convo(1), 6).is_none());
    }

    // ── assembler ─────────────────────────────────────────────────────────────

    #[test]
    fn assemble_shape_summary_block_then_recent_verbatim() {
        let recent = convo(3);
        let out = assemble_compressed("the summary", &recent);
        assert_eq!(out.len(), 4);
        assert_eq!(out[0].role, "system");
        assert!(out[0].content.contains("the summary"));
        assert!(out[0].content.contains("Summary of the earlier conversation"));
        assert_eq!(&out[1..], &recent[..]);
    }

    // ── compression orchestrator (injected summariser, no network) ───────────

    #[test]
    fn compress_under_threshold_is_identity_and_never_calls_gp() {
        let turns = convo(10);
        let out = compress_context_with(&turns, usize::MAX, 6, |_| {
            panic!("summarizer must not be called under threshold")
        });
        assert_eq!(out, turns);
    }

    #[test]
    fn compress_nothing_to_split_is_identity_and_never_calls_gp() {
        // Over threshold (0) but only keep_last turns exist → nothing older.
        let turns = convo(6);
        let out = compress_context_with(&turns, 0, 6, |_| {
            panic!("summarizer must not be called with nothing to compress")
        });
        assert_eq!(out, turns);
    }

    #[test]
    fn compress_success_replaces_older_with_summary_block() {
        let turns = convo(10);
        let out = compress_context_with(&turns, 0, 6, |prompt| {
            // The prompt must carry the older turns, not the recent ones.
            assert!(prompt.contains("turn 0"));
            assert!(prompt.contains("turn 3"));
            assert!(!prompt.contains("turn 4"));
            Some("condensed".into())
        });
        assert_eq!(out.len(), 7); // 1 summary + 6 recent
        assert_eq!(out[0].role, "system");
        assert!(out[0].content.contains("condensed"));
        assert_eq!(&out[1..], &turns[4..]);
    }

    #[test]
    fn compress_gp_failure_returns_original_unchanged() {
        let turns = convo(10);
        let out = compress_context_with(&turns, 0, 6, |_| None);
        assert_eq!(out, turns, "GP failure must return the ORIGINAL messages");
    }

    #[test]
    fn compress_empty_summary_returns_original_unchanged() {
        let turns = convo(10);
        let out = compress_context_with(&turns, 0, 6, |_| Some("   \n".into()));
        assert_eq!(out, turns, "blank summary is a failure, not a replacement");
    }

    // ── system-prompt injection ───────────────────────────────────────────────

    fn full_ctx() -> ProjectContext {
        ProjectContext {
            claude_md_summary: Some("Use pnpm. TS strict.".into()),
            active_task: Some("Ship E2-S2".into()),
            stack_notes: Some("Rust workspace, axum daemon.".into()),
        }
    }

    #[test]
    fn injection_two_calls_same_input_identical_bytes() {
        let ctx = full_ctx();
        let a = build_system_prefix(&ctx).expect("some");
        let b = build_system_prefix(&ctx).expect("some");
        assert_eq!(a.as_bytes(), b.as_bytes(), "must be byte-stable (cache-prefix-safe)");
    }

    #[test]
    fn injection_stable_ordering_summary_task_notes() {
        let s = build_system_prefix(&full_ctx()).expect("some");
        let summary_pos = s.find("Project instructions").expect("summary section");
        let task_pos = s.find("Active task").expect("task section");
        let notes_pos = s.find("Stack notes").expect("notes section");
        assert!(summary_pos < task_pos && task_pos < notes_pos, "fixed order: {s}");
    }

    #[test]
    fn injection_contains_no_time_varying_content() {
        let s = build_system_prefix(&full_ctx()).expect("some");
        // No current-date leakage of any common shape (YYYY-, unix-epoch digits).
        assert!(!s.contains("202"), "no timestamps allowed: {s}");
    }

    #[test]
    fn injection_all_empty_is_none() {
        assert_eq!(build_system_prefix(&ProjectContext::default()), None);
        let blank = ProjectContext {
            claude_md_summary: Some("   ".into()),
            active_task: Some(String::new()),
            stack_notes: None,
        };
        assert_eq!(build_system_prefix(&blank), None);
    }

    #[test]
    fn injection_partial_fields_only_render_present_sections() {
        let ctx = ProjectContext {
            claude_md_summary: None,
            active_task: Some("Fix the bug".into()),
            stack_notes: None,
        };
        let s = build_system_prefix(&ctx).expect("some");
        assert!(s.contains("Active task"));
        assert!(!s.contains("Project instructions"));
        assert!(!s.contains("Stack notes"));
    }

    // ── classification parser ─────────────────────────────────────────────────

    #[test]
    fn parse_clean_labels() {
        assert_eq!(parse_classification("trivial"), Some(RequestComplexity::Trivial));
        assert_eq!(parse_classification(" Medium\n"), Some(RequestComplexity::Medium));
        assert_eq!(parse_classification("COMPLEX."), Some(RequestComplexity::Complex));
    }

    #[test]
    fn parse_lenient_extracts_from_prose() {
        assert_eq!(
            parse_classification("I would say this is medium difficulty."),
            Some(RequestComplexity::Medium)
        );
        // Earliest occurrence wins when the model rambles.
        assert_eq!(
            parse_classification("complex... though arguably trivial"),
            Some(RequestComplexity::Complex)
        );
    }

    #[test]
    fn parse_garbage_and_empty_are_none() {
        assert_eq!(parse_classification(""), None);
        assert_eq!(parse_classification("banana"), None);
        assert_eq!(parse_classification("<html>502</html>"), None);
    }

    // ── classification orchestrator (injected completion, no network) ────────

    #[test]
    fn classify_with_failure_is_none() {
        assert_eq!(classify_with("do a thing", |_| None), None);
    }

    #[test]
    fn classify_with_garbage_reply_is_none() {
        assert_eq!(classify_with("do a thing", |_| Some("banana".into())), None);
    }

    #[test]
    fn classify_with_parses_reply_not_the_prompt() {
        // The user prompt contains "complex"; only the REPLY must be parsed.
        let got = classify_with("this is a complex request", |_| Some("trivial".into()));
        assert_eq!(got, Some(RequestComplexity::Trivial));
    }

    // ── tier mapping ──────────────────────────────────────────────────────────

    #[test]
    fn tier_mapping_all_three() {
        assert_eq!(tier_for_complexity(RequestComplexity::Trivial), Tier::T3);
        assert_eq!(tier_for_complexity(RequestComplexity::Medium), Tier::T2);
        assert_eq!(tier_for_complexity(RequestComplexity::Complex), Tier::T1);
    }

    // ── GP sensitivity firewall (gates run BEFORE any network I/O) ───────────

    #[test]
    fn gp_may_process_blocks_sensitive_prompts() {
        assert!(!gp_may_process("my ssn is 123-45-6789"));
        assert!(!gp_may_process("my va disability rating went up"));
        assert!(!gp_may_process("set ANTHROPIC_API_KEY=sk-ant-abc in the env"));
        assert!(gp_may_process("refactor the auth module to be smaller"));
    }

    /// A summary prompt embedding sensitive conversation content must never
    /// be summarised via GP: the firewall rejects it before any network I/O
    /// (this test would hang/flake against a live proxy otherwise) and the
    /// ORIGINAL messages come back unchanged.
    #[test]
    fn compress_via_gp_sensitive_content_returns_original_without_network() {
        let filler = "x".repeat(8_000);
        let mut turns: Vec<ChatTurn> = (0..8)
            .map(|i| turn(if i % 2 == 0 { "user" } else { "assistant" }, &filler))
            .collect();
        // Sensitive line in the OLDER (to-be-summarised) region.
        turns[0].content = format!("my ssn is 123-45-6789. {filler}");
        assert!(estimate_tokens(&turns) > DEFAULT_COMPRESS_THRESHOLD_TOKENS);

        let out = compress_context_via_gp(&turns);
        assert_eq!(out, turns, "sensitive history must never be GP-compressed");
    }

    /// Classification of a sensitive user prompt is refused pre-network:
    /// the caller keeps its own tier/model choice (`None`).
    #[test]
    fn classify_prompt_sensitive_content_is_none_without_network() {
        assert_eq!(classify_prompt("draft my va claim with ssn 123-45-6789"), None);
    }
}
