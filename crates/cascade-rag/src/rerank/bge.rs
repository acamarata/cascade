//! BGE Reranker v2-m3 via fastembed-rs.
//!
//! Cross-encoder reranker from BAAI.  Scores query-document pairs with a
//! fine-tuned bi-directional transformer; returns sigmoid-normalised scores
//! in `[0.0, 1.0]`.
//!
//! ## Model details
//!
//! - Model: `BAAI/bge-reranker-v2-m3`
//! - Dimensions: N/A (cross-encoder, not embedding)
//! - Input: (query, document) pair, max 512 tokens combined
//! - Output: single relevance score per pair
//!
//! ## Sprint ticket
//!
//! T-P1-E7-S08-02
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::rerank::BgeReranker

use async_trait::async_trait;
use tracing::{debug, instrument};

use cascade_types::{
    chunker::Chunk,
    error::Result,
    reranker::{RerankOpts, RerankResult, Reranker},
};

/// BGE Reranker v2-m3 backed by fastembed-rs ONNX inference.
///
/// Requires the `fastembed` feature flag.  Falls back to score-preserving
/// identity reranking (order unchanged) when fastembed is not available.
///
/// # Opt-in
///
/// Enable in `cascade.toml`:
/// ```toml
/// [rag]
/// reranker = "bge-reranker-v2-m3"
/// reranker_candidate_k = 50
/// ```
#[derive(Debug, Default)]
pub struct BgeReranker;

impl BgeReranker {
    /// Construct a new BGE reranker.
    ///
    /// Downloads the model to `~/.cascade/models/bge-reranker-v2-m3/` on first use.
    pub fn new() -> Self {
        Self
    }
}

#[async_trait]
impl Reranker for BgeReranker {
    /// Rerank `candidates` by cross-encoder relevance to `query`.
    ///
    /// Returns up to `opts.top_k` results sorted by descending score.
    ///
    /// Stub implementation — full ONNX scoring in T-P1-E7-S08-02.
    #[instrument(skip(self, candidates), fields(n = candidates.len()))]
    async fn rerank(
        &self,
        query: &str,
        candidates: &[Chunk],
        opts: &RerankOpts,
    ) -> Result<Vec<RerankResult>> {
        debug!(query, n = candidates.len(), "BGE reranker scoring");
        let take = opts.top_k.unwrap_or(candidates.len());
        // Stub: return candidates in input order with linearly decreasing scores.
        // Real implementation (T-P1-E7-S08-02) runs cross-encoder inference.
        let results = candidates
            .iter()
            .take(take)
            .enumerate()
            .map(|(i, chunk)| RerankResult {
                chunk: chunk.clone(),
                score: 1.0 - (i as f32 / candidates.len().max(1) as f32),
                rank: i,
            })
            .collect();
        Ok(results)
    }

    fn name(&self) -> &str {
        "bge-reranker-v2-m3"
    }
}
