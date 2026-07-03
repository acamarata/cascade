//! # middleware — request pre/post-processing around provider dispatch
//!
//! ## Purpose
//!
//! E2-S2 "spend GP to save premium tokens": optional, quality-guarded
//! pre-processing steps that run between a chat/completion request arriving
//! and the provider dispatch. Every feature is OFF by default and each one is
//! individually flag-gated by the daemon's `[middleware]` config section —
//! when all flags are off the daemon never touches this module (zero added
//! latency, no GP probes).
//!
//! | Feature | Flag | What it does |
//! |---|---|---|
//! | Context compression | `middleware.compress_context` | GP-summarize old turns, keep last N verbatim |
//! | System-prompt injection | `middleware.inject_context` | pure template merge of project context |
//! | Request classification | `middleware.classify_requests` | GP call → Trivial/Medium/Complex → Tier |
//! | Context sync (POST) | `middleware.context_sync` | background response digest → context-sync JSONL (E2-S3) |
//!
//! Pre-processing lives in [`pre`]; POST-processing (E2-S3) lives in [`post`]
//! and runs ONLY in a background task spawned after the response has been
//! delivered — never on the hot path.
//!
//! ## Quality guard (hard contract)
//!
//! GP output is a *savings* optimisation, never a correctness dependency: on
//! ANY GP failure (proxy down, timeout, error envelope, garbage reply) every
//! function here returns the caller's original input unchanged (compression)
//! or `None` (classification). Correctness is never degraded for savings.
//!
//! ## Privacy guard (hard contract)
//!
//! Every GP call here embeds conversation content verbatim, so BEFORE any
//! network I/O two gates apply: the `CASCADE_GFP_LANE_OFF` kill switch (same
//! switch as `routing::delegate::GfpLane`) and the sensitivity firewall
//! ([`pre::gp_may_process`], the canonical `SensitivityPolicy` against the
//! external `gfp` provider). Sensitive content stays on the machine and the
//! caller falls back to its no-GP behaviour.
//!
//! ## Constraints
//!
//! - All splitter/assembler/template/parse functions are pure (no I/O, no
//!   clock reads) and unit-tested without network.
//! - The only I/O is the bounded GP call via the existing
//!   [`crate::routing::gfp_http`] transport — no new HTTP client.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: middleware (E2-S2)

pub mod post;
pub mod pre;

pub use post::{
    extract_completed_tasks, extract_decisions, extract_digest, extract_digest_taxonomy,
    extract_digest_with, extract_files_touched, extract_path_candidates, redact_secrets,
    to_jsonl_line, ResponseDigest, REDACTED,
};
pub use pre::{
    assemble_compressed, build_system_prefix, classification_prompt, classify_prompt,
    classify_with, compress_context_via_gp, compress_context_with, estimate_tokens,
    gp_may_process, parse_classification, split_for_compression, summary_prompt,
    tier_for_complexity, ChatTurn, ProjectContext, RequestComplexity, CHARS_PER_TOKEN,
    DEFAULT_COMPRESS_THRESHOLD_TOKENS, KEEP_LAST_TURNS,
};
