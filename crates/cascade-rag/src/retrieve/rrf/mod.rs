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

mod config;
mod merge;
mod retriever;

#[cfg(test)]
mod tests;

// Re-export all public items at the original path so callers are unaffected.
pub use config::RrfConfig;
pub use merge::{rrf_merge, FusedHit, RankedList};
pub use retriever::RrfRetriever;
