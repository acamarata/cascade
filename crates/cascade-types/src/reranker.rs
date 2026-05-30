//! Reranker trait and associated types.
//!
//! A `Reranker` takes an initial candidate list from a [`crate::retriever::Retriever`]
//! and returns the same candidates ordered by a more accurate cross-encoder
//! relevance score. Reranking is typically called after a cheap first-pass
//! vector search to improve precision at the top of the list.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use crate::chunker::Chunk;
use crate::error::Result;

// ── RerankResult ──────────────────────────────────────────────────────────────

/// A reranked chunk with its new relevance score.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RerankResult {
    /// The chunk that was reranked.
    pub chunk: Chunk,

    /// The cross-encoder relevance score for this chunk.
    ///
    /// Higher is more relevant. The exact scale is model-dependent;
    /// typical cross-encoders emit a value in `[0.0, 1.0]` after sigmoid.
    pub score: f32,

    /// 0-based rank in the reranked list. `rank == 0` is the most relevant.
    pub rank: usize,
}

// ── RerankOpts ────────────────────────────────────────────────────────────────

/// Options for a reranking call.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RerankOpts {
    /// Maximum number of results to return after reranking.
    /// If `None`, all candidates are returned (reordered).
    pub top_k: Option<usize>,

    /// Minimum score threshold. Results below this value are dropped.
    pub min_score: Option<f32>,
}

impl Default for RerankOpts {
    fn default() -> Self {
        Self {
            top_k: Some(5),
            min_score: None,
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Reorders a list of candidate chunks by cross-encoder relevance.
///
/// Implementations may call a remote API (Cohere Rerank, Jina Reranker, …)
/// or run a local cross-encoder model.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_types::reranker::{Reranker, RerankResult, RerankOpts};
/// # use cascade_types::chunker::Chunk;
/// # use cascade_types::error::Result;
/// # use async_trait::async_trait;
/// struct CohereReranker;
///
/// #[async_trait]
/// impl Reranker for CohereReranker {
///     async fn rerank(
///         &self,
///         query: &str,
///         candidates: &[Chunk],
///         opts: &RerankOpts,
///     ) -> Result<Vec<RerankResult>> {
///         // call Cohere Rerank API …
///         Ok(vec![])
///     }
/// }
/// ```
#[async_trait]
pub trait Reranker: Send + Sync {
    /// Rerank `candidates` for the given `query`.
    ///
    /// The returned vector is sorted by `score` descending (most relevant first)
    /// and `rank` is set to the 0-based position in that order.
    ///
    /// Callers may pass more candidates than `opts.top_k` to trade off
    /// recall vs. reranking latency.
    async fn rerank(
        &self,
        query: &str,
        candidates: &[Chunk],
        opts: &RerankOpts,
    ) -> Result<Vec<RerankResult>>;

    /// Human-readable name for this reranker (used in logging and config).
    fn name(&self) -> &str {
        "unknown-reranker"
    }
}

// ── No-op implementation (for tests) ─────────────────────────────────────────

/// Returns candidates unchanged with scores in descending insertion order.
///
/// This is useful as a stand-in when no reranker is configured.
#[derive(Debug, Default)]
pub struct NoopReranker;

#[async_trait]
impl Reranker for NoopReranker {
    async fn rerank(
        &self,
        _query: &str,
        candidates: &[Chunk],
        opts: &RerankOpts,
    ) -> Result<Vec<RerankResult>> {
        let take = opts.top_k.unwrap_or(candidates.len());
        let results = candidates
            .iter()
            .take(take)
            .enumerate()
            .map(|(i, chunk)| RerankResult {
                chunk: chunk.clone(),
                // Descending mock score: first candidate gets 1.0, last gets ~0.0.
                score: 1.0 - (i as f32 / candidates.len().max(1) as f32),
                rank: i,
            })
            .collect();
        Ok(results)
    }

    fn name(&self) -> &str {
        "noop"
    }
}

fn _assert_object_safe(_: &dyn Reranker) {}
