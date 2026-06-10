//! HyDE: Hypothetical Document Embeddings query strategy.
//!
//! Given a user query, an LLM generates a hypothetical answer to that query.
//! The hypothetical answer is then embedded and used as the query vector for
//! ANN retrieval.  HyDE consistently outperforms direct query embedding on
//! domain-specific corpora by bridging the vocabulary gap between queries
//! ("what is X?") and documents ("X is defined as…").
//!
//! ## Algorithm (S12-02)
//!
//! 1. Call the connected LLM (via `cascade.sampling`) with a "generate a
//!    one-paragraph answer to this question" prompt.
//! 2. Embed the returned hypothetical answer with the active `EmbeddingProvider`.
//! 3. Use the embedding as the ANN query vector in `VectorRetriever`.
//!
//! ## Latency constraint (S12-07)
//!
//! Total query latency target < 500 ms including the LLM call.  Mark opt-in if
//! consistently > 500 ms.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve::HydeRetriever

use async_trait::async_trait;
use tracing::{debug, instrument};

use cascade_types::{
    error::Result,
    query_strategy::{ExpandedQuery, QueryStrategy, StrategyKind},
};

/// HyDE query strategy: expands a user query into a hypothetical answer.
///
/// The expanded query string is then passed to [`super::vector::VectorRetriever`]
/// for embedding-based retrieval.
///
/// Requires a connected LLM (via MCP Sampling or a local model); returns the
/// original query unchanged when no LLM is available.
#[derive(Debug, Default)]
pub struct HydeStrategy;

#[async_trait]
impl QueryStrategy for HydeStrategy {
    fn kind(&self) -> StrategyKind {
        StrategyKind::Hyde
    }

    /// Expand `query` by generating a hypothetical answer.
    ///
    /// The default implementation returns the original query unchanged
    /// (fallback when no LLM is configured).  The real implementation
    /// (T-P1-E7-S12-02) calls the LLM via MCP Sampling.
    #[instrument(skip(self))]
    async fn expand(&self, query: &str) -> Result<ExpandedQuery> {
        debug!(query, "HyDE: generating hypothetical document");
        // TODO(T-P1-E7-S12-02): call LLM via cascade Sampling to generate a
        // hypothetical answer, then return the answer text as the expanded query.
        Ok(ExpandedQuery {
            queries: vec![query.to_string()],
            filters: None,
            strategy: StrategyKind::Hyde,
        })
    }
}
