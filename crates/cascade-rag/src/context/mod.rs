//! Context optimization module for cascade-rag.
//!
//! Provides shell-output compression, sliding token-window budgeting, and
//! chunk deduplication — both within-window (T-P4-E04-20) and cross-session
//! (T-P4-E04-21) — before context is serialised and sent to a harness.
//!
//! ## Pipeline (applied in order)
//!
//! 1. **`dedup`** — within-window content-fingerprint dedup (SHA-256 of
//!    normalised text). Eliminates identical or near-identical chunks from the
//!    same retrieval pass.
//! 2. **`window`** — greedy sliding window: keep highest-scoring chunks until
//!    the token budget is reached.
//! 3. **`compress_shell`** — ANSI stripping, consecutive-line folding, and
//!    head+tail truncation for stdout/shell blocks.
//!
//! ## Token estimation
//!
//! Tokens are approximated as `words * 1.33` where words is
//! `text.split_whitespace().count()`. This avoids a tiktoken dependency while
//! staying within ±15% for typical prose and code.
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-rag/context

pub mod compressor;

pub use compressor::{ContextOptimizer, ContextResult};
