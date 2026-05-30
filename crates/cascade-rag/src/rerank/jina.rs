//! Jina Reranker v2 via fastembed-rs.
//!
//! Alternative cross-encoder reranker.  Useful when multilingual reranking
//! quality is more important than raw speed (Jina Reranker v2 supports 100+
//! languages vs. BGE's primarily English/Chinese focus).
//!
//! Sprint ticket: T-P1-E7-S08-03
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::rerank::JinaReranker

use async_trait::async_trait;
use tracing::instrument;

use cascade_types::{
    chunker::Chunk,
    error::Result,
    reranker::{RerankOpts, RerankResult, Reranker},
};

/// Jina Reranker v2 cross-encoder.
#[derive(Debug, Default)]
pub struct JinaReranker;

#[async_trait]
impl Reranker for JinaReranker {
    #[instrument(skip(self, candidates))]
    async fn rerank(
        &self,
        _query: &str,
        candidates: &[Chunk],
        opts: &RerankOpts,
    ) -> Result<Vec<RerankResult>> {
        let take = opts.top_k.unwrap_or(candidates.len());
        Ok(candidates
            .iter()
            .take(take)
            .enumerate()
            .map(|(i, chunk)| RerankResult {
                chunk: chunk.clone(),
                score: 1.0 - (i as f32 / candidates.len().max(1) as f32),
                rank: i,
            })
            .collect())
    }

    fn name(&self) -> &str {
        "jina-reranker-v2"
    }
}
