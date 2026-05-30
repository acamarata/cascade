//! Hybrid Reciprocal Rank Fusion (RRF) retriever.
//!
//! Combines FTS5 BM25 keyword results and sqlite-vec dense ANN results using
//! Reciprocal Rank Fusion.  RRF is rank-based (not score-based), which makes
//! it robust to score distribution differences between retrieval engines.
//!
//! ## RRF formula (S07-01)
//!
//! ```text
//! rrf_score(d) = Σ_i  1 / (k + rank_i(d))
//! ```
//!
//! where `k = 60` (configurable), `rank_i(d)` is the 1-based rank of document
//! `d` in the i-th result list, and the sum is over all retrieval engines.
//! Documents not appearing in a result list are treated as having rank = ∞
//! (contributing 0 to the sum).
//!
//! ## Performance target (S07-06)
//!
//! Hybrid query p95 < 200 ms at K=10 on a 100K-chunk index.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve::RrfRetriever

use async_trait::async_trait;
use std::collections::HashMap;
use std::sync::Arc;
use tracing::instrument;

use cascade_types::{
    error::Result,
    retriever::{RetrievalHit, RetrieveOpts, Retriever},
    EmbeddingProvider,
};

use crate::index::RagIndex;
use super::fts::FtsRetriever;
use super::vector::VectorRetriever;

/// Default RRF smoothing constant.  60 is the canonical default from the
/// original Cormack et al. 2009 paper.
const DEFAULT_K: f32 = 60.0;

/// Configuration for the RRF fusion step.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct RrfConfig {
    /// RRF smoothing constant.  Larger values reduce the influence of top-1
    /// results relative to the rest of the list.
    pub k: f32,
    /// Whether to use FTS5 results.
    pub use_fts: bool,
    /// Whether to use dense vector results.
    pub use_vec: bool,
}

impl Default for RrfConfig {
    fn default() -> Self {
        Self {
            k: DEFAULT_K,
            use_fts: true,
            use_vec: true,
        }
    }
}

/// Hybrid FTS5 + dense-vector retriever fused with Reciprocal Rank Fusion.
///
/// This is the default retriever for `TierLevel::Semantic` and above.
///
/// When `use_vec = false` (or no embedding provider is given), falls back to
/// pure FTS5.  When `use_fts = false`, falls back to pure vector search.
///
/// # Example
///
/// ```rust,no_run
/// # use std::sync::Arc;
/// # use cascade_rag::index::RagIndex;
/// # use cascade_rag::retrieve::rrf::{RrfRetriever, RrfConfig};
/// # use cascade_types::NoopEmbeddingProvider;
/// # use cascade_types::Retriever;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let idx = Arc::new(RagIndex::open("/tmp/cascade.db").await?);
/// let embedder = Arc::new(NoopEmbeddingProvider);
/// let retriever = RrfRetriever::new(idx, embedder, RrfConfig::default());
/// let hits = retriever.retrieve("prayer time", &Default::default()).await?;
/// # Ok(())
/// # }
/// ```
pub struct RrfRetriever {
    fts: FtsRetriever,
    vec: Option<VectorRetriever>,
    config: RrfConfig,
}

impl RrfRetriever {
    /// Construct an RRF retriever with both FTS5 and dense-vector legs.
    pub fn new(
        index: Arc<RagIndex>,
        embedder: Arc<dyn EmbeddingProvider>,
        config: RrfConfig,
    ) -> Self {
        let fts = FtsRetriever::new(Arc::clone(&index));
        let vec = if config.use_vec {
            Some(VectorRetriever::new(index, embedder))
        } else {
            None
        };
        Self { fts, vec, config }
    }

    /// Construct an FTS-only retriever (no embeddings required).
    pub fn fts_only(index: Arc<RagIndex>) -> Self {
        let fts = FtsRetriever::new(index);
        Self {
            fts,
            vec: None,
            config: RrfConfig {
                use_vec: false,
                ..Default::default()
            },
        }
    }
}

#[async_trait]
impl Retriever for RrfRetriever {
    /// Execute FTS5 and ANN queries in parallel, fuse with RRF, return top-K.
    ///
    /// If the vector leg is not configured (FTS-only mode), returns BM25 hits
    /// directly without score transformation.
    #[instrument(skip(self), fields(k = opts.k))]
    async fn retrieve(&self, query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
        // Fetch candidates from both legs (overfetch to ensure good RRF coverage).
        let candidate_k = (opts.k * 5).max(50);
        let fts_opts = RetrieveOpts {
            k: candidate_k,
            ..opts.clone()
        };

        let fts_hits = if self.config.use_fts {
            self.fts.retrieve(query, &fts_opts).await?
        } else {
            vec![]
        };

        let vec_hits = if let (true, Some(vec_ret)) = (self.config.use_vec, &self.vec) {
            vec_ret.retrieve(query, &fts_opts).await?
        } else {
            vec![]
        };

        // If only one leg contributed, return its results directly.
        if fts_hits.is_empty() {
            return Ok(vec_hits.into_iter().take(opts.k).collect());
        }
        if vec_hits.is_empty() {
            return Ok(fts_hits.into_iter().take(opts.k).collect());
        }

        // Fuse with RRF.
        let mut scores: HashMap<String, f32> = HashMap::new();
        let k = self.config.k;

        for (rank, hit) in fts_hits.iter().enumerate() {
            *scores.entry(hit.chunk_id.clone()).or_insert(0.0) +=
                1.0 / (k + (rank + 1) as f32);
        }
        for (rank, hit) in vec_hits.iter().enumerate() {
            *scores.entry(hit.chunk_id.clone()).or_insert(0.0) +=
                1.0 / (k + (rank + 1) as f32);
        }

        // Build a unified hit index from FTS results (text is available there).
        let mut hit_map: HashMap<String, RetrievalHit> = fts_hits
            .into_iter()
            .map(|h| (h.chunk_id.clone(), h))
            .collect();
        for h in vec_hits {
            hit_map.entry(h.chunk_id.clone()).or_insert(h);
        }

        // Sort by RRF score descending.
        let mut ranked: Vec<(String, f32)> = scores.into_iter().collect();
        ranked.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));

        let results: Vec<RetrievalHit> = ranked
            .into_iter()
            .take(opts.k)
            .enumerate()
            .filter_map(|(new_rank, (chunk_id, rrf_score))| {
                hit_map.remove(&chunk_id).map(|mut hit| {
                    hit.score = rrf_score;
                    hit.rank = new_rank;
                    hit
                })
            })
            .collect();

        Ok(results)
    }

    fn name(&self) -> &str {
        "hybrid-rrf"
    }
}
